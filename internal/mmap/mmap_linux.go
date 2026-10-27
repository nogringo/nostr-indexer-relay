// Package mmap gives the kernel access hints for the files the relay maps in memory.
package mmap

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// AdviseRandom tells the kernel that every mapping of file is read at random, as LMDB does
// for MDB_NORDAHEAD. Otherwise each page fault also reads the pages around it, which evicts
// from a full page cache the pages that the next lookups need.
func AdviseRandom(file string) error {
	file, err := filepath.Abs(file)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(file); err == nil {
		file = resolved
	}
	maps, err := os.ReadFile("/proc/self/maps")
	if err != nil {
		return err
	}

	advised := false
	for line := range strings.Lines(string(maps)) {
		line = strings.TrimSuffix(line, "\n")
		if !strings.HasSuffix(line, " "+file) {
			continue
		}
		start, end, _ := strings.Cut(strings.Fields(line)[0], "-")
		lo, err := strconv.ParseUint(start, 16, 64)
		if err != nil {
			return err
		}
		hi, err := strconv.ParseUint(end, 16, 64)
		if err != nil {
			return err
		}
		if _, _, errno := syscall.Syscall(syscall.SYS_MADVISE, uintptr(lo), uintptr(hi-lo), syscall.MADV_RANDOM); errno != 0 {
			return fmt.Errorf("madvise on %s: %w", file, errno)
		}
		advised = true
	}
	if !advised {
		return fmt.Errorf("%s is not mapped", file)
	}
	return nil
}
