package main

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/html"
)

const (
	RequestTimeout = time.Second * 60
	Workers        = 8
)

type queueRequest struct {
	req  string
	resp chan *ResponseItem
}

type fetcherServer struct {
	queue chan *queueRequest
	done  chan bool
}

func newQueueRequest(url string) *queueRequest {
	return &queueRequest{
		req:  url,
		resp: make(chan *ResponseItem),
	}
}

func (r *queueRequest) do(fs *fetcherServer) *ResponseItem {
	fs.queue <- r
	return <-r.resp
}

// create new fetcher backend
func newFetcher() (*fetcherServer, error) {

	fs := &fetcherServer{
		queue: make(chan *queueRequest, 64),
		done:  make(chan bool, Workers),
	}
	for i := 0; i < Workers; i++ {
		go fs.worker()
	}
	return fs, nil
}

func (fs *fetcherServer) stop() {
	for i := 0; i < Workers; i++ {
		fs.done <- true
	}
}

// GIN handler
func (fs *fetcherServer) handle(c *gin.Context) {

	var request Request

	// decode request
	err := c.BindJSON(&request)
	if err != nil {
		log.Println("Error decoding request: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
	}

	// call handler
	result, err := fs.do(request)
	if err != nil {
		log.Println("Unrecoverable error while fetching the request: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
	}

	c.JSON(http.StatusOK, result)
}

func (fs *fetcherServer) do(urls []string) (*Response, error) {

	var (
		result = make(chan *ResponseItem, len(urls))
		wg     sync.WaitGroup
	)

	wg.Add(len(urls))

	for _, param := range urls {
		go func(url string) {
			// url is passed as a parameter to create
			// a copy from the loop one
			resp := newQueueRequest(url).do(fs)
			result <- resp

			wg.Done()
		}(param)
	}

	// close channel on completion
	go func() {
		wg.Wait()
		close(result)
	}()

	var output = Response([]*ResponseItem{})
	for item := range result {
		output = append(output, item)
	}

	return &output, nil
}

func (fs *fetcherServer) worker() {

	select {
	case in := <-fs.queue:
		// do the fetching
		res, err := fs.work(in.req)
		if err != nil {
			// error feedback is wanted
			res = &ResponseItem{
				URL: in.req,
				Meta: Meta{
					Status: http.StatusInternalServerError,
					Error:  err.Error(),
				},
			}
		}

		in.resp <- res

	case <-fs.done:
		return
	}
}

func (fs *fetcherServer) work(url string) (*ResponseItem, error) {

	// do GET with timeout
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()

	client := &http.Client{}

	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}

	var result = ResponseItem{
		URL: url,
		Meta: Meta{
			Status: resp.StatusCode,
		},
	}

	// fill in content type if code is 2xx
	if !(resp.StatusCode >= 200 && resp.StatusCode < 300) {
		err = resp.Body.Close()
		return &result, err
	}

	result.Meta.ContentType = resp.Header.Get("Content-Type")

	// read body
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	result.Meta.ContentLength = len(body)

	// abort if content is empty or not html
	if result.Meta.ContentLength == 0 ||
		!strings.HasPrefix(result.Meta.ContentType, "text/html") {

		return &result, nil
	}

	// count && fill-in tags
	tags, err := countTags(body)
	if err != nil {
		// just HTML errors; ignore
		log.Println("HTML parse error: %v", err)
	} else {
		result.Elements = tags
	}

	return &result, nil
}

// count all html-tags in input document
//  <p>lol</p> is one p element
func countTags(body []byte) ([]Element, error) {

	var (
		counts = map[string]int{}
		reader = bytes.NewBuffer(body)
		z      = html.NewTokenizer(reader)
	)

	for {
		switch z.Next() {
		case html.ErrorToken:
			if z.Err() == io.EOF {
				// this is the return-point
				return encodeTags(counts), nil
			}

			// any other err is unexpected
			log.Printf("html token err: %v", z.Err())
			return nil, z.Err()

		case html.StartTagToken, html.SelfClosingTagToken:
			tagName, _ := z.TagName()
			counts[string(tagName)] += 1
		}
	}

}

// encode counts map to a response array
func encodeTags(counts map[string]int) []Element {
	var (
		result = make([]Element, len(counts))
		iter   int
	)

	// decode it to result
	for name, count := range counts {
		result[iter] = Element{
			TagName: name,
			Count:   count,
		}
		iter++
	}

	return result
}
