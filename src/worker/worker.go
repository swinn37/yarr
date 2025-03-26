package worker

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nkanaev/yarr/src/storage"
)

// Increase the number of workers based on available CPU cores
var NUM_WORKERS = 4

type Worker struct {
	db      *storage.Storage
	pending *int32
	refresh *time.Ticker
	reflock sync.Mutex
	stopper chan bool
}

func NewWorker(db *storage.Storage) *Worker {
	pending := int32(0)
	return &Worker{db: db, pending: &pending}
}

func (w *Worker) FeedsPending() int32 {
	// Ensure we never return a negative value
	pending := atomic.LoadInt32(w.pending)
	if pending < 0 {
		atomic.StoreInt32(w.pending, 0)
		return 0
	}
	return pending
}

func (w *Worker) StartFeedCleaner() {
	go w.db.DeleteOldItems()
	ticker := time.NewTicker(time.Hour * 24)
	go func() {
		for {
			<-ticker.C
			w.db.DeleteOldItems()
		}
	}()
}

func (w *Worker) FindFavicons() {
	go func() {
		for _, feed := range w.db.ListFeedsMissingIcons() {
			w.FindFeedFavicon(feed)
		}
	}()
}

func (w *Worker) FindFeedFavicon(feed storage.Feed) {
	// Create a context with a reasonable timeout for favicon fetching
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	icon, err := findFaviconWithContext(ctx, feed.Link, feed.FeedLink)
	if err != nil {
		log.Printf("Failed to find favicon for %s (%s): %s", feed.FeedLink, feed.Link, err)
	}
	if icon != nil {
		w.db.UpdateFeedIcon(feed.Id, icon)
	}
}

func (w *Worker) SetRefreshRate(minute int64) {
	if w.stopper != nil {
		w.refresh.Stop()
		w.refresh = nil
		w.stopper <- true
		w.stopper = nil
	}

	if minute == 0 {
		return
	}

	w.stopper = make(chan bool)
	w.refresh = time.NewTicker(time.Minute * time.Duration(minute))

	go func(fire <-chan time.Time, stop <-chan bool, m int64) {
		log.Printf("auto-refresh %dm: starting", m)
		for {
			select {
			case <-fire:
				log.Printf("auto-refresh %dm: firing", m)
				w.RefreshFeeds()
			case <-stop:
				log.Printf("auto-refresh %dm: stopping", m)
				return
			}
		}
	}(w.refresh.C, w.stopper, minute)
}

func (w *Worker) RefreshFeeds() {
	w.reflock.Lock()
	defer w.reflock.Unlock()

	if *w.pending > 0 {
		log.Print("Refreshing already in progress")
		return
	}

	feeds := w.db.ListFeeds()
	if len(feeds) == 0 {
		log.Print("Nothing to refresh")
		return
	}

	log.Print("Refreshing feeds")
	atomic.StoreInt32(w.pending, int32(len(feeds)))
	go w.refresher(feeds)
}

func (w *Worker) StopRefresh() {
	w.reflock.Lock()
	defer w.reflock.Unlock()

	if *w.pending > 0 {
		log.Print("Stopping refresh in progress")
		atomic.StoreInt32(w.pending, 0)
	}
}

func (w *Worker) refresher(feeds []storage.Feed) {
	w.db.ResetFeedErrors()

	// Create buffered channels for better throughput
	srcqueue := make(chan storage.Feed, len(feeds))
	dstqueue := make(chan []storage.Item, NUM_WORKERS)

	// Use a WaitGroup to manage worker goroutines
	var wg sync.WaitGroup

	// Start workers
	for i := 0; i < NUM_WORKERS; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.worker(srcqueue, dstqueue)
		}()
	}

	// Queue all feeds for processing
	for _, feed := range feeds {
		srcqueue <- feed
	}
	// Close the source queue to signal no more feeds
	close(srcqueue)

	// Start a goroutine to close the destination queue when all workers are done
	go func() {
		wg.Wait()
		close(dstqueue)
	}()

	// Process results as they come in
	for items := range dstqueue {
		if len(items) > 0 {
			w.db.CreateItems(items)
		}

		// Update progress counter
		// Ensure pending never goes below 0
		current := atomic.LoadInt32(w.pending)
		if current > 0 {
			atomic.AddInt32(w.pending, -1)
		}
	}

	// Final sync
	w.db.SyncSearch()

	// Ensure pending is exactly 0 when finished
	atomic.StoreInt32(w.pending, 0)
	log.Printf("Finished refreshing %d feeds", len(feeds))

	// Add debug output for failed feeds
	feedErrors := w.db.GetFeedErrors()
	if len(feedErrors) > 0 {
		log.Printf("Failed to refresh %d feeds:", len(feedErrors))

		// Create a map of feed IDs to feed titles for easier lookup
		feedTitles := make(map[int64]string)
		for _, feed := range feeds {
			feedTitles[feed.Id] = feed.Title
		}

		// Log each failed feed with its title and error message
		for feedId, errMsg := range feedErrors {
			title := feedTitles[feedId]
			if title == "" {
				title = "<unknown>"
			}
			log.Printf("  - %s (ID: %d): %s", title, feedId, errMsg)
		}
	}
}

func (w *Worker) worker(srcqueue <-chan storage.Feed, dstqueue chan<- []storage.Item) {
	for feed := range srcqueue {
		items, err := listItems(feed, w.db)
		if err != nil {
			w.db.SetFeedError(feed.Id, err)
		}
		if items != nil {
			dstqueue <- items
		} else {
			// Send empty slice to maintain feed count
			dstqueue <- []storage.Item{}
		}
	}
}
