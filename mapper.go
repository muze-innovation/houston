package houston

import "sync"

// CodeMapper maps an Problem to the numeric API code embedded in the JSON response body.
// Numeric codes are a repository-level concern — configure once at app startup via SetMapper.
type CodeMapper interface {
	Map(err Problem) int32
}

var (
	globalMapper CodeMapper
	mapperMu     sync.RWMutex
)

// SetMapper registers the global CodeMapper. Call once in main() before serving requests.
func SetMapper(m CodeMapper) {
	mapperMu.Lock()
	globalMapper = m
	mapperMu.Unlock()
}

// ResolveHTTPStatus returns the HTTP status code for the given Problem.
// Convenience wrapper used by the response layer.
func ResolveHTTPStatus(err Problem) int {
	return err.HTTPStatus()
}

// ResolveCode returns the numeric API code for err using the registered mapper.
// Returns 0 if no mapper has been set (response layer should omit the code field).
func ResolveCode(err Problem) int32 {
	mapperMu.RLock()
	m := globalMapper
	mapperMu.RUnlock()
	if m == nil {
		return 0
	}
	return m.Map(err)
}

// DefaultMapper maps Kind constants to numeric API codes.
// Ships with no defaults — the app must supply the mapping.
// HTTP status codes are NOT configurable here; they are fixed per constructor.
type DefaultMapper struct {
	mu           sync.RWMutex
	codes        map[string]int32
	bizFallback  int32
	techFallback int32
}

// NewDefaultMapper creates a DefaultMapper from a kind→numeric-code map.
// Keys should be houston.Kind* constants.
func NewDefaultMapper(codes map[string]int32) *DefaultMapper {
	cp := make(map[string]int32, len(codes))
	for k, v := range codes {
		cp[k] = v
	}
	return &DefaultMapper{codes: cp}
}

// Override replaces or adds a single kind→numeric-code entry.
// Fluent — returns the same mapper for chaining.
// Safe to call concurrently with Map.
func (m *DefaultMapper) Override(kind string, code int32) *DefaultMapper {
	m.mu.Lock()
	m.codes[kind] = code
	m.mu.Unlock()
	return m
}

// WithFallback sets the fallback numeric codes for kinds not present in the map.
// bizCode is used when IsBusiness()=true, techCode when IsBusiness()=false.
func (m *DefaultMapper) WithFallback(bizCode, techCode int32) *DefaultMapper {
	m.bizFallback = bizCode
	m.techFallback = techCode
	return m
}

// Map implements CodeMapper.
func (m *DefaultMapper) Map(err Problem) int32 {
	m.mu.RLock()
	code, ok := m.codes[err.Kind()]
	m.mu.RUnlock()
	if ok {
		return code
	}
	if err.IsBusiness() {
		return m.bizFallback
	}
	return m.techFallback
}
