package main

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/jakobilobi/wadjit"
)

func main() {
	// Create the wadjit - the manager of all watchers
	manager := wadjit.New()
	defer manager.Close()

	// Create a watcher that sends HTTP requests to get the current time in London and Singapore
	timeWatcher, err := wadjit.NewWatcher(
		"my time watcher",
		8*time.Second,
		timeTasks(),
	)
	if err != nil {
		fmt.Printf("Error creating watcher: %v\n", err)
		return
	}

	// Add the watcher to the wadjit
	err = manager.AddWatcher(timeWatcher)
	if err != nil {
		fmt.Printf("Error adding watcher: %v\n", err)
		return
	}

	// Start consuming responses, this also triggers the watchers to start
	respChannel := manager.Start()
	for {
		resp, ok := <-respChannel
		if !ok {
			fmt.Println("Channel closed")
			break
		}
		fmt.Printf("Response from %v\n", resp.URL)
		fmt.Printf("  Watcher ID: %v\n", resp.WatcherID)
		if resp.Err != nil {
			fmt.Printf("  Error: %v\n", resp.Err)
			continue
		}
		data, err := resp.Data()
		if err != nil {
			fmt.Printf("Error reading data: %v\n", err)
			continue
		}
		fmt.Printf("Data: %s\n", data)
		fmt.Printf("Metadata:\n")
		//fmt.Printf("  Sent at:     %v\n", resp.Metadata().TimeSent)
		fmt.Printf("  Received at: %v\n", resp.Metadata().TimeReceived)
		fmt.Printf("  Latency: %v\n", resp.Metadata().Latency)
		fmt.Println()
	}
}

func timeTasks() []wadjit.WatcherTask {
	londonTimeTask := &wadjit.HTTPEndpoint{
		Header:  make(http.Header),
		Method:  http.MethodGet,
		Payload: nil,
		URL: &url.URL{Scheme: "https",
			Host:     "www.timeapi.io",
			Path:     "/api/time/current/zone",
			RawQuery: "timeZone=Europe%2FLondon",
		},
	}
	singaporeTimeTask := &wadjit.HTTPEndpoint{
		Header:  make(http.Header),
		Method:  http.MethodGet,
		Payload: nil,
		URL: &url.URL{Scheme: "https",
			Host:     "www.timeapi.io",
			Path:     "/api/time/current/zone",
			RawQuery: "timeZone=Asia%2FSingapore",
		},
	}

	tasks := wadjit.WatcherTasksToSlice(londonTimeTask, singaporeTimeTask)

	return tasks
}
