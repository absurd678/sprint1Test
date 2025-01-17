package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/absurd678/skill/cmd/config"
	"github.com/absurd678/skill/internal/models"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

var mapURLmain = map[string]string{
	"sharaga": "https://mai.ru",
}

const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"
const shortURLsize int = 10

// ----------------------STRUCTURES----------------------------
type (
	Connection struct {
		mapURL map[string]string
	}

	// Logging
	LogData struct { // the field of logResponse
		code int
		size int
	}

	ResLogOrCompress struct { // to log response data
		res  http.ResponseWriter
		data *LogData
		gz   *gzip.Writer // compress data
	}
	// Logging

	// Decompress
	Decompress struct {
		rc io.ReadCloser
		gz *gzip.Reader // decompress data
	}
)

// ----------------------logResponse-------------------------------
func (lc *ResLogOrCompress) Write(b []byte) (int, error) {

	var size int
	var err error

	if lc.gz != nil { // if the compression initiated
		size, err = lc.gz.Write(b) // compress first
	} else {
		size, err = lc.res.Write(b) // no compression
	}

	if err != nil {
		return 0, err
	}
	lc.data.size += size
	return size, nil
}

func (lc *ResLogOrCompress) WriteHeader(StatusCode int) {
	lc.res.WriteHeader(StatusCode)
	lc.data.code = StatusCode
}

func (lc *ResLogOrCompress) Header() http.Header {
	return lc.res.Header()
}

//-----------------------logResponse------------------------------

// ------------------------Decompress-----------------------------
func newDecompress(init_rc io.ReadCloser) (*Decompress, error) {
	rd, err := gzip.NewReader(init_rc)
	return &Decompress{
		rc: init_rc,
		gz: rd,
	}, err
}

func (d *Decompress) Read(p []byte) (int, error) {
	return d.gz.Read(p)
}

func (d *Decompress) Close() error {
	if err := d.rc.Close(); err != nil {
		return err
	}
	return d.gz.Close()
}

// ------------------------Decompress-----------------------------

// RandString generates a random string with the given length
func RandString(n int) string {
	// rand.Seed is deprecated, use NewSource instead :D
	r := rand.New(rand.NewSource(time.Now().Unix()))
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[r.Intn(len(letterBytes))]
	}
	return string(b)
}

// ------------------------Connection-----------------------------
func (c *Connection) GetHandler(res http.ResponseWriter, req *http.Request) {
	// take /{id} and search for value in the map
	shortURL := chi.URLParam(req, "id")
	original, ok := c.mapURL[shortURL]
	if !ok {
		res.WriteHeader(http.StatusBadRequest) // DOESN'T WORK to fill code field for logResponse
		res.Write([]byte("Invalid URL for GET"))
		return
	}

	// Add the Location header with original URL
	res.Header().Add("Location", original) // No location actually sent. However the header is added.
	res.WriteHeader(http.StatusTemporaryRedirect)
	res.Write([]byte(""))
}

func (c *Connection) PostHandler(res http.ResponseWriter, req *http.Request) {
	// Get the URL from the body (and the new id also) like this: localhost:8080 -d https://example
	original, err := io.ReadAll(req.Body)
	if err != nil {
		res.WriteHeader(http.StatusBadRequest) // to fill code field for logResponse
		res.Write([]byte("Invalid URL for POST"))
		return
	}
	// get the new id from the b flag
	c.mapURL[config.UrlID] = string(original)

	res.WriteHeader(http.StatusCreated)
	// Body answer: localhost:8080/{id}
	res.Write([]byte(req.URL.Path + config.UrlID))
}

func (c *Connection) PostHandlerJSON(res http.ResponseWriter, req *http.Request) {
	// get json: {"url": "some_url"}
	// return json: {"result": "short_url"}
	var some_url models.SomeURL
	var short_url models.ShortURL
	var buff []byte
	var err error

	if err = json.NewDecoder(req.Body).Decode(&some_url); err != nil {
		res.WriteHeader(http.StatusBadRequest)
		return
	}
	short_url = models.ShortURL{URL: config.UrlID}
	c.mapURL[short_url.URL] = some_url.URL
	res.WriteHeader(http.StatusCreated)
	if buff, err = json.MarshalIndent(short_url, "", " "); err != nil {
		res.WriteHeader(http.StatusBadRequest)
		res.Write([]byte("Unmarshable data"))
		return
	}
	res.Write(buff)
}

// ------------------------Connection-----------------------------

func checkURL(next http.Handler) http.Handler { // to avoid paths like localhost:8080/{id}/extrapath

	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {

		// compression variables

		var wgzip *gzip.Writer
		var rgzip *Decompress

		// Logging setup
		middlewareLogger, err := zap.NewDevelopment()
		if err != nil {
			http.Error(res, "Logger error", http.StatusInternalServerError)
		}
		sugarLogger := middlewareLogger.Sugar() // for JSON-like messages
		// Logging request
		sugarLogger.Infow("Request parameters",
			"URI", req.RequestURI,
			"Method", req.Method,
		)

		// Check Accept-Encoding
		if strings.Contains(req.Header.Get("Accept-Encoding"), "gzip") {
			var err error
			wgzip, err = gzip.NewWriterLevel(res, gzip.BestSpeed)
			res.Header().Set("Content-Encoding", "gzip")

			if err != nil {
				sugarLogger.Error("Error creating gzip writer")
				http.Error(res, "Error creating gzip writer", http.StatusInternalServerError)
				return
			}
			defer wgzip.Close() // Send all the data!
		}

		// !Check Content-Encoding
		if strings.Contains(req.Header.Get("Content-Encoding"), "gzip") {
			rgzip, err = newDecompress(req.Body)
			if err != nil {
				sugarLogger.Error("Error creating gzip reader")
				http.Error(res, "Error creating gzip reader", http.StatusInternalServerError)
				return
			}
			req.Body = rgzip
			defer rgzip.Close()
		}

		// ResponseWriter implementation
		logRW := &ResLogOrCompress{res, &LogData{code: 0, size: 0}, wgzip}
		timeDuration := time.Now() // query duration

		// Handlers
		if req.Method == http.MethodGet && regexp.MustCompile(`^/[a-zA-Z0-9-]+$`).MatchString(req.URL.Path) {
			next.ServeHTTP(logRW, req)
		} else if req.Method == http.MethodPost && req.URL.Path == "/" {
			next.ServeHTTP(logRW, req)
		} else if req.Method == http.MethodPost && req.URL.Path == "/api/shorten" {
			next.ServeHTTP(logRW, req)
		} else {
			http.Error(res, "Invalid URL", http.StatusBadRequest)
			logRW.WriteHeader(http.StatusBadRequest)
			logRW.Write([]byte("Invalid URL"))
		}

		// Logging response
		sugarLogger.Infow(
			"Response parameters",
			"Status Code", logRW.data.code,
			"Size", logRW.data.size,
			"Duration", time.Since(timeDuration),
		)
	})
}

func LaunchMyRouter(c *Connection) chi.Router {
	myRouter := chi.NewRouter()
	myRouter.Use(checkURL)
	myRouter.Get("/{id}", c.GetHandler)
	myRouter.Post("/", c.PostHandler)
	myRouter.Post("/api/shorten", c.PostHandlerJSON)

	return myRouter
}

func main() {

	c := &Connection{mapURLmain}

	config.ParseFlags() // read a and b flags for host:port and {id} information

	err := http.ListenAndServe(config.HostFlags.String(), LaunchMyRouter(c))
	if err != nil {
		panic(err)
	}
}
