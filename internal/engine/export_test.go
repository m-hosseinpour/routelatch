package engine

// ConfigForTest exposes the generated core document to the external test
// package without widening the production API.
func (e *Engine) ConfigForTest() []byte { return e.rawConfig }
