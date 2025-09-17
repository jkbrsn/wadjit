package wadjit

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TODO: potentially rename file as "task_test.go"

type watcherTaskTestSuite struct {
	newTask func() WatcherTask
}

func (s *watcherTaskTestSuite) TestWatcherTaskInitialize(t *testing.T) {
	task := s.newTask()
	responseChan := make(chan WatcherResponse, 1)

	err := task.Initialize("test-watcher-id", responseChan)
	assert.NoError(t, err)
}

func (s *watcherTaskTestSuite) TestWatcherTaskTask(t *testing.T) {
	task := s.newTask()
	responseChan := make(chan WatcherResponse, 1)

	err := task.Initialize("test-watcher-id", responseChan)
	assert.NoError(t, err)

	tk := task.Task()
	assert.NotNil(t, tk)
}

func (s *watcherTaskTestSuite) TestWatcherTaskValidate(t *testing.T) {
	task := s.newTask()
	responseChan := make(chan WatcherResponse, 1)

	err := task.Initialize("test-watcher-id", responseChan)
	assert.NoError(t, err)

	err = task.Validate()
	assert.NoError(t, err)
}

func (s *watcherTaskTestSuite) TestWatcherTaskClose(t *testing.T) {
	task := s.newTask()
	responseChan := make(chan WatcherResponse, 1)

	err := task.Initialize("test-watcher-id", responseChan)
	assert.NoError(t, err)

	err = task.Close()
	assert.NoError(t, err)
}

func runWatcherTaskTestSuite(t *testing.T, s *watcherTaskTestSuite) {
	t.Run("Initialize", s.TestWatcherTaskInitialize)
	t.Run("Task", s.TestWatcherTaskTask)
	t.Run("Validate", s.TestWatcherTaskValidate)
	t.Run("Close", s.TestWatcherTaskClose)
}

func TestWatcherTaskSuite(t *testing.T) {
	testURL, _ := url.Parse("http://example.com")

	t.Run("HTTPEndpoint", func(t *testing.T) {
		newTask := func() WatcherTask {
			return NewHTTPEndpoint(testURL, http.MethodGet, WithID("test-id"))
		}
		runWatcherTaskTestSuite(t, &watcherTaskTestSuite{newTask: newTask})
	})

	t.Run("WSEndpoint", func(t *testing.T) {
		newTask := func() WatcherTask {
			return NewWSEndpoint(testURL, http.Header{}, OneHitText, nil, "test-id")
		}
		runWatcherTaskTestSuite(t, &watcherTaskTestSuite{newTask: newTask})
	})
}
