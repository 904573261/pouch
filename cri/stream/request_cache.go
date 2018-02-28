package stream

import (
	"container/list"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math"
	"sync"
	"time"
)

var (
	// Timeout after which tokens become invalid.
	CacheTTL = 1 * time.Minute
	// The maximum number of in-flight requests to allow.
	MaxInFlight = 1000
	// Length of the random base64 encoded token identifying the request.
	TokenLen = 8
)

// requestCache caches streaming (exec/attach/port-forward) requests and generates a single-use
// random token for their retrieval. The requestCache is used for building streaming URLs without
// the need to encode every request parameter in the URL.
type requestCache struct {
	// tokens maps the generate token to the request for fast retrieval.
	tokens map[string]*list.Element
	// ll maintains an age-ordered request list for faster garbage collection of expired requests.
	ll *list.List

	lock sync.Mutex
}

// Type representing an *ExecRequest, *AttachRequest, or *PortForwardRequest.
type request interface{}

type cacheEntry struct {
	token      string
	req        request
	expireTime time.Time
}

func newRequestCache() *requestCache {
	return &requestCache{
		ll:     list.New(),
		tokens: make(map[string]*list.Element),
	}
}

// Insert the given request into the cache and returns the token used for fetching it out.
func (c *requestCache) Insert(req request) (token string, err error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	// Remove expired entries.
	c.gc()
	// If the cache is full, reject the request.
	if c.ll.Len() == MaxInFlight {
		return "", ErrorTooManyInFlight()
	}
	token, err = c.uniqueToken()
	if err != nil {
		return "", err
	}
	// 将cache entry加入list
	ele := c.ll.PushFront(&cacheEntry{token, req, time.Now().Add(CacheTTL)})

	c.tokens[token] = ele
	return token, nil
}

// Consume the token (remove it from the cache) and return the cached request, if found.
func (c *requestCache) Consume(token string) (req request, found bool) {
	c.lock.Lock()
	defer c.lock.Unlock()
	ele, ok := c.tokens[token]
	if !ok {
		return nil, false
	}
	c.ll.Remove(ele)
	delete(c.tokens, token)

	entry := ele.Value.(*cacheEntry)
	if time.Now().After(entry.expireTime) {
		// Entry already expired.
		return nil, false
	}
	return entry.req, true
}

// uniqueToken generates a random URL-safe token and ensures uniqueness.
func (c *requestCache) uniqueToken() (string, error) {
	const maxTries = 10
	// Number of bytes to be TokenLen when base64 encoded.
	tokenSize := math.Ceil(float64(TokenLen) * 6 / 8)
	rawToken := make([]byte, int(tokenSize))
	for i := 0; i < maxTries; i++ {
		if _, err := rand.Read(rawToken); err != nil {
			return "", err
		}
		encoded := base64.RawURLEncoding.EncodeToString(rawToken)
		token := encoded[:TokenLen]
		// If it's unique, return it. Otherwise retry.
		if _, exists := c.tokens[encoded]; !exists {
			return token, nil
		}
	}
	return "", fmt.Errorf("failed to generate unique token")
}

// Must be write-locked prior to calling.
func (c *requestCache) gc() {
	now := time.Now()
	for c.ll.Len() > 0 {
		oldest := c.ll.Back()
		entry := oldest.Value.(*cacheEntry)
		if !now.After(entry.expireTime) {
			return
		}

		// Oldest value is expired; remove it.
		c.ll.Remove(oldest)
		delete(c.tokens, entry.token)
	}
}
