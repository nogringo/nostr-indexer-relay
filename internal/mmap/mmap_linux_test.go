package mmap

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestAdviseRandom(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.mdb")
	if err := os.WriteFile(path, make([]byte, 1<<20), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := syscall.Mmap(int(f.Fd()), 0, 1<<20, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Munmap(data)

	if randomRead(t, path) {
		t.Fatal("the mapping is flagged for random reads before any advice")
	}
	if err := AdviseRandom(path); err != nil {
		t.Fatal(err)
	}
	if !randomRead(t, path) {
		t.Fatal("the mapping is not flagged for random reads")
	}
}

func TestAdviseRandomUnmapped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.mdb")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := AdviseRandom(path); err == nil {
		t.Fatal("no error for a file that is not mapped")
	}
}

// randomRead reports whether the kernel flags the mapping of path with rr, for MADV_RANDOM.
func randomRead(t *testing.T, path string) bool {
	smaps, err := os.ReadFile("/proc/self/smaps")
	if err != nil {
		t.Fatal(err)
	}
	inMapping := false
	for line := range strings.Lines(string(smaps)) {
		line = strings.TrimSuffix(line, "\n")
		if strings.HasSuffix(line, " "+path) {
			inMapping = true
		}
		if inMapping && strings.HasPrefix(line, "VmFlags:") {
			return strings.Contains(line+" ", " rr ")
		}
	}
	t.Fatalf("%s is not mapped", path)
	return false
}
