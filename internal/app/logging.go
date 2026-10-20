package app

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DailyLogWriter rolls logs at midnight or 20 MiB. It never records request bodies.
type DailyLogWriter struct {
	Dir  string
	mu   sync.Mutex
	file *os.File
	day  string
	size int64
}

func (w *DailyLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	day := time.Now().Format("2006-01-02")
	if w.file == nil || day != w.day || w.size+int64(len(p)) > 20<<20 {
		if w.file != nil {
			w.file.Close()
			w.file = nil
		}
		name := "gmpan-" + day + ".jsonl"
		path := filepath.Join(w.Dir, name)
		if info, e := os.Stat(path); e == nil && info.Size()+int64(len(p)) > 20<<20 {
			path = filepath.Join(w.Dir, "gmpan-"+day+"-"+time.Now().Format("150405.000000000")+".jsonl")
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return 0, e
		}
		w.file = f
		w.day = day
		info, e := f.Stat()
		if e != nil {
			return 0, e
		}
		w.size = info.Size()
	}
	n, e := w.file.Write(p)
	w.size += int64(n)
	return n, e
}
func (w *DailyLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}
