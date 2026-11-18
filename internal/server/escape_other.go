//go:build !linux

package server

// WatchLocalKeyboards: local keyboards are watched on Linux only.
func (s *Server) WatchLocalKeyboards() {}
