/**
 * Tenta DNS Server
 *
 *    Copyright 2017 Tenta, LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * For any questions, please contact developer@tenta.io
 *
 * cache.go: Provides a simple dns resolver focused cache
 */

package runtime

import (
	"bytes"
	"net/http"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/tenta-browser/tenta-dns/log"
)

type DNSValMap map[uint16][]*DNSCacheRecord

type DNSCache struct {
	l sync.RWMutex             /// global lock, used exclusively for key creation/deletion
	c map[string]*DNSCacheItem /// the actual cache, with added "per-row" mutex
}

type DNSCacheItem struct {
	l sync.RWMutex /// lock for the item
	v DNSValMap    /// map to possible values
}

// DNSCacheRecord -- for the time being this will only contain dns.RR as a record
type DNSCacheRecord struct {
	dns.RR
}

func newDNSCacheRecord(rr dns.RR) *DNSCacheRecord {
	return &DNSCacheRecord{rr}
}

// AsRR -- gets a DNSCacheRecord reconstructed as dns.RR
func (r *DNSCacheRecord) asRR() dns.RR {
	return dns.RR(r)
}

// AsRRArray -- gets a map section and transforms it into []dns.RR, ready to be returned for host application (no sync inside)
func (v DNSValMap) asRRArray(key uint16) (ret []dns.RR) {
	ret = []dns.RR{}

	for _, rr := range v[key] {
		ret = append(ret, rr.asRR())
	}

	return
}

// Add -- standard behavior element addition, does not check for pre-existence, or coalesce saved values
func (c *DNSCache) Add(key string, value dns.RR) {
	c.l.Lock()
	if _, ok := c.c[key]; !ok {
		c.c[key] = new(DNSCacheItem)
	}
	c.c[key].l.Lock()
	defer c.c[key].l.Unlock()
	c.l.Unlock()
	temp := c.c[key].v[value.Header().Rrtype]
	temp = append(temp, newDNSCacheRecord(value))
}

// AddExt -- custom addition, by means of a function argument, up to the implementer's discretion, which will be executed instead of the classic insertion
func (c *DNSCache) AddExt(key string, value dns.RR, fn func(key string, value dns.RR, vmap DNSValMap) bool) {
	c.l.Lock()
	if _, ok := c.c[key]; !ok {
		c.c[key] = new(DNSCacheItem)
	}
	c.c[key].l.Lock()
	defer c.c[key].l.Unlock()
	c.l.Unlock()
	if !fn(key, value, c.c[key].v) {
		temp := c.c[key].v[value.Header().Rrtype]
		temp = append(temp, newDNSCacheRecord(value))
	}
}

func (c *DNSCache) Get(key string, rrType uint16) ([]dns.RR, error) {
	c.l.RLock()
	if _, ok := c.c[key]; !ok {
		c.l.RUnlock()
		return []dns.RR{}, nil
	}
	temp := c.c[key]
	temp.l.RLock()
	defer temp.l.RUnlock()
	c.l.RUnlock()
	return temp.v.asRRArray(rrType), nil
}

func (c *DNSCache) GetRaw(key string, rrType uint16) (DNSValMap, error) {
	c.l.RLock()
	if _, ok := c.c[key]; !ok {
		c.l.RUnlock()
		return nil, nil
	}
	temp := c.c[key]
	temp.l.RLock()
	defer temp.l.RUnlock()
	c.l.RUnlock()
	return temp.v, nil
}

func StartDNSCache(cfg Config, rt *Runtime) *DNSCache {
	f := &DNSCache{}
	if t, ok := cfg.SlackFeedback["url"]; !ok {
		f.nopmode = true
	} else {
		f.msg = make(chan []byte)
		f.stop = make(chan bool)
		f.whURL = t
		if nodeId, ok := cfg.SlackFeedback["node"]; ok {
			f.nodeId = nodeId
		} else {
			f.nodeId = "placeholder"
		}
		f.l = log.GetLogger("feedback")
		wg := &sync.WaitGroup{}
		f.wg = wg
		go f.startFeedbackService()
		f.wg.Add(1)
		f.l.Infof("Started Feedback service")
	}
	return f
}

func (f *DNSCache) startDNSCacheService() {
	defer f.wg.Done()
	for {
		select {
		case <-f.stop:
			f.l.Infof("Stop signal received. Exiting.")
			return
		case b := <-f.msg:
			resp, err := http.Post(f.whURL, "application/x-www-form-urlencoded", bytes.NewReader(b))
			defer resp.Body.Close()
			if err != nil {
				f.l.Infof("Unable to send to Slack. Cause [%s]", err.Error())
			} else if resp.StatusCode != 200 {
				f.l.Infof("Unable to send to Slack. HTTP status [%d]", resp.StatusCode)
			}
			break
		case <-time.After(100 * time.Millisecond):
			break
		}
	}
}

func (f *DNSCache) Stop() {
	if !f.nopmode {
		defer f.wg.Wait()
		f.stop <- true
	}
}
