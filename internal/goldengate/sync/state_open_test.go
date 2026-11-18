package sync

import (
	"path/filepath"
	gosync "sync"
	"testing"
)

// Several requests of the web UI can open a state database that does not
// exist yet at the same moment; every one of them must succeed (#140).
func TestOpenStateConcurrentFirstOpen(t *testing.T) {
	for round := 0; round < 20; round++ {
		path := filepath.Join(t.TempDir(), "state.db")
		const n = 8
		var wg gosync.WaitGroup
		errs := make(chan error, n)
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				st, err := OpenState(path)
				if err != nil {
					errs <- err
					return
				}
				_ = st.Close()
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: %v", round, err)
		}
	}
}
