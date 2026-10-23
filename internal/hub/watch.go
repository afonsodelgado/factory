package hub

import (
	"sync"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	C chan struct{}

	fs      *fsnotify.Watcher
	mu      sync.Mutex
	watched map[string]bool
}

func NewWatcher() (*Watcher, error) {
	fs, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{C: make(chan struct{}, 1), fs: fs, watched: map[string]bool{}}
	go w.run()
	return w, nil
}

func (w *Watcher) run() {
	for {
		select {
		case _, ok := <-w.fs.Events:
			if !ok {
				return
			}
			select {
			case w.C <- struct{}{}:
			default:
			}
		case _, ok := <-w.fs.Errors:
			if !ok {
				return
			}
		}
	}
}

func (w *Watcher) Watch(dirs []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, dir := range dirs {
		if dir == "" || w.watched[dir] {
			continue
		}
		if w.fs.Add(dir) == nil {
			w.watched[dir] = true
		}
	}
}

func (w *Watcher) Close() error {
	return w.fs.Close()
}
