package storage

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
)

// EventBroker coordinates broadcasting SSE updates to connected dashboard clients
type EventBroker struct {
	clients        map[chan string]bool
	newClients     chan chan string
	defunctClients chan chan string
	broadcast      chan string
	mu             sync.Mutex
}

var (
	GlobalBroker *EventBroker
	brokerOnce   sync.Once
)

// GetBroker returns the singleton event broadcaster
func GetBroker() *EventBroker {
	brokerOnce.Do(func() {
		GlobalBroker = &EventBroker{
			clients:        make(map[chan string]bool),
			newClients:     make(chan chan string),
			defunctClients: make(chan chan string),
			broadcast:      make(chan string, 100),
		}
		go GlobalBroker.listen()
	})
	return GlobalBroker
}

func (b *EventBroker) listen() {
	for {
		select {
		case s := <-b.newClients:
			b.mu.Lock()
			b.clients[s] = true
			b.mu.Unlock()

		case s := <-b.defunctClients:
			b.mu.Lock()
			delete(b.clients, s)
			close(s)
			b.mu.Unlock()

		case msg := <-b.broadcast:
			b.mu.Lock()
			for clientChan := range b.clients {
				select {
				case clientChan <- msg:
				default:
					// Drop event if client buffer is full to prevent daemon stall
				}
			}
			b.mu.Unlock()
		}
	}
}

// BroadcastQueueState emits a JSON array of all current jobs to all SSE connections
func (b *EventBroker) BroadcastQueueState(jobs []models.UIJob) {
	data, err := json.Marshal(jobs)
	if err != nil {
		return
	}
	b.broadcast <- fmt.Sprintf("event: queue\ndata: %s\n\n", string(data))
}

// ServeHTTP handles incoming SSE client subscriptions
func (b *EventBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	messageChan := make(chan string, 32)
	b.newClients <- messageChan

	defer func() {
		b.defunctClients <- messageChan
	}()

	// Send initial state on connection
	dbStore, err := GetDBStore()
	if err == nil {
		jobs := dbStore.GetAllJobs()
		if initData, err := json.Marshal(jobs); err == nil {
			fmt.Fprintf(w, "event: queue\ndata: %s\n\n", string(initData))
			flusher.Flush()
		}
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case msg := <-messageChan:
			fmt.Fprint(w, msg)
			flusher.Flush()

		case <-ticker.C:
			// Heartbeat comment ping to prevent connection timeout
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
