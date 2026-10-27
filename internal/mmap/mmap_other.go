//go:build !linux

package mmap

// AdviseRandom does nothing outside Linux, where the relay is deployed.
func AdviseRandom(string) error { return nil }
