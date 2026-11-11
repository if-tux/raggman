// Command smoke checks a running Raggman end to end with real models: it
// submits a small PDF, waits until it is Ready, asks a Question, expects an
// Answer citing that PDF, and removes the PDF again so the next run ingests it
// afresh. Run it with `make smoke`; it is not part of CI because real model
// output is not deterministic.
package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
)

// manual.pdf: two Pages of English text describing Raggman itself.
//
//go:embed manual.pdf
var manual []byte

const question = "Which model generates the answers?"

// client bounds every request except the streamed Answer, which can take
// minutes on CPU and uses the overall deadline instead.
var client = &http.Client{Timeout: 30 * time.Second}

func main() {
	baseURL := flag.String("url", "http://localhost:8080", "Raggman base URL")
	timeout := flag.Duration("timeout", 10*time.Minute, "overall time limit")
	flag.Parse()

	deadline := time.Now().Add(*timeout)
	if err := run(strings.TrimSuffix(*baseURL, "/"), deadline); err != nil {
		fmt.Fprintln(os.Stderr, "smoke: FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("smoke: OK")
}

func run(baseURL string, deadline time.Time) error {
	start := time.Now()
	id, created, err := submit(baseURL)
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("smoke: submitted manual.pdf (%s)\n", id)
		defer remove(baseURL, id)
	} else {
		fmt.Printf("smoke: manual.pdf was already submitted (%s); Ingestion is not re-run\n", id)
	}

	if err := waitReady(baseURL, id, deadline); err != nil {
		return err
	}
	fmt.Printf("smoke: Ready after %s\n", time.Since(start).Round(time.Second))

	start = time.Now()
	answer, citations, err := ask(baseURL, time.Until(deadline))
	if err != nil {
		return err
	}
	fmt.Printf("smoke: Q: %s\nsmoke: A: %s\nsmoke: answered in %s\n", question, answer, time.Since(start).Round(time.Second))

	cited := false
	for _, c := range citations {
		fmt.Printf("smoke: Citation [%d] %s, page %d\n", c.Marker, c.Filename, c.Page)
		cited = cited || c.DocumentID == id
	}
	if !cited {
		return errors.New("the Answer does not cite the submitted Document")
	}
	return nil
}

// checkStatus returns an error with the API's message unless resp has one
// of the wanted status codes.
func checkStatus(resp *http.Response, what string, want ...int) error {
	for _, code := range want {
		if resp.StatusCode == code {
			return nil
		}
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("%s: %s: %s", what, resp.Status, bytes.TrimSpace(msg))
}

// submit sends manual.pdf; created is false when the same PDF was already there.
func submit(baseURL string) (id string, created bool, err error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "manual.pdf")
	if err != nil {
		return "", false, err
	}
	if _, err := part.Write(manual); err != nil {
		return "", false, err
	}
	if err := form.Close(); err != nil {
		return "", false, err
	}

	resp, err := client.Post(baseURL+"/documents", form.FormDataContentType(), &body)
	if err != nil {
		return "", false, fmt.Errorf("is Raggman running at %s? %w", baseURL, err)
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, "POST /documents", http.StatusCreated, http.StatusOK); err != nil {
		return "", false, err
	}
	var doc struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", false, err
	}
	return doc.ID, resp.StatusCode == http.StatusCreated, nil
}

// remove deletes the Document the smoke created; failures are only reported.
func remove(baseURL, id string) {
	req, err := http.NewRequest(http.MethodDelete, baseURL+"/documents/"+id, nil)
	if err == nil {
		var resp *http.Response
		if resp, err = client.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "smoke: could not remove manual.pdf:", err)
		return
	}
	fmt.Println("smoke: removed manual.pdf")
}

func waitReady(baseURL, id string, deadline time.Time) error {
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/documents/" + id)
		if err != nil {
			return err
		}
		var doc struct {
			Status        string `json:"status"`
			FailureReason string `json:"failure_reason"`
		}
		err = checkStatus(resp, "GET /documents/"+id, http.StatusOK)
		if err == nil {
			err = json.NewDecoder(resp.Body).Decode(&doc)
		}
		resp.Body.Close()
		if err != nil {
			return err
		}
		switch doc.Status {
		case "READY":
			return nil
		case "FAILED":
			return fmt.Errorf("ingestion failed: %s", doc.FailureReason)
		}
		time.Sleep(time.Second)
	}
	return errors.New("the Document was not Ready before the deadline")
}

type citation struct {
	Filename   string `json:"filename"`
	DocumentID string `json:"document_id"`
	Marker     int    `json:"marker"`
	Page       int    `json:"page"`
}

func ask(baseURL string, timeout time.Duration) (string, []citation, error) {
	payload, err := json.Marshal(map[string]string{"question": question})
	if err != nil {
		return "", nil, err
	}
	streaming := &http.Client{Timeout: timeout}
	resp, err := streaming.Post(baseURL+"/questions", "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, "POST /questions", http.StatusOK); err != nil {
		return "", nil, err
	}

	var answer strings.Builder
	var event string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			event = name
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		switch event {
		case "token":
			var token struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(data), &token); err != nil {
				return "", nil, err
			}
			answer.WriteString(token.Text)
		case "citations":
			var final struct {
				Citations []citation `json:"citations"`
			}
			err := json.Unmarshal([]byte(data), &final)
			return strings.TrimSpace(answer.String()), final.Citations, err
		case "error":
			return "", nil, fmt.Errorf("answer failed: %s", data)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", nil, fmt.Errorf("reading the Answer stream: %w", err)
	}
	return "", nil, errors.New("the Answer stream ended without Citations")
}
