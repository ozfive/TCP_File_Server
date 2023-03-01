package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultBufferSize is the default buffer size used for reading/writing files.
	DefaultBufferSize = 8192

	// DefaultConcurrentConnections is the default number of concurrent connections that can be handled.
	DefaultConcurrentConnections = 10

	// ConnectionTimeout is the timeout for connections.
	ConnectionTimeout = 30 * time.Second
)

var (
	// logger is used for logging errors.
	logger = log.New(os.Stderr, "", log.LstdFlags)
)

// serveFile serves a file to a client over a TCP connection.
func serveFile(conn net.Conn, filename string, bufferSize int) {
	file, err := os.Open(filename)
	if err != nil {
		logger.Printf("Error serving file: %s", err)
		return
	}
	defer func() {
		if err := file.Close(); err != nil {
			logger.Printf("Error closing file: %s", err)
		}
	}()

	buffer := make([]byte, bufferSize)
	for {
		bytesRead, err := file.Read(buffer)
		if err != nil {
			if err != io.EOF {
				logger.Printf("Error serving file: %s", err)
			}
			break
		}

		_, err = conn.Write(buffer[:bytesRead])
		if err != nil {
			logger.Printf("Error serving file: %s", err)
			break
		}
	}
}

// startServer starts a TCP server that serves files to clients.
func startServer(ctx context.Context, rootDir string, port int, numConnections int, bufferSize int) {
	connLogFile, err := os.OpenFile("connections.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		logger.Fatalf("Failed to create connections log file: %s", err)
	}
	defer connLogFile.Close()

	connLogger := log.New(connLogFile, "", log.LstdFlags)

	errLogFile, err := os.OpenFile("errors.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		logger.Fatalf("Failed to create errors log file: %s", err)
	}
	defer errLogFile.Close()

	errLogger := log.New(errLogFile, "", log.LstdFlags)

	if _, err := os.Stat(rootDir); err != nil {
		errLogger.Fatalf("Failed to access root directory: %s", err)
	}

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		errLogger.Fatalf("Failed to start listener: %s", err)
	}
	defer listener.Close()

	connLogger.Printf("Server listening on port %d...", port)

	var wg sync.WaitGroup
	var wgMutex sync.Mutex
	sem := make(chan struct{}, numConnections)

	for {
		select {
		case <-ctx.Done():
			connLogger.Printf("Server shutting down...")
			return
		default:
			sem <- struct{}{}
			conn, err := listener.Accept()
			if err != nil {
				errLogger.Printf("Failed to accept connection: %s", err)
				<-sem
				continue
			}

			wgMutex.Lock()
			wg.Add(1)
			wgMutex.Unlock()

			go func() {
				defer func() {
					if r := recover(); r != nil {
						errLogger.Printf("Panic in handleConnection: %v", r)
					}
					connLogger.Printf("Closed connection from %s", conn.RemoteAddr().String())
					conn.Close()

					wgMutex.Lock()
					wg.Done()
					<-sem
					wgMutex.Unlock()
				}()

				connLogger.Printf("Accepted connection from %s", conn.RemoteAddr().String())
				handleConnection(ctx, conn, rootDir, bufferSize)
			}()
		}
	}
}

func handleConnection(ctx context.Context, conn net.Conn, rootDir string, bufferSize int) {
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(ConnectionTimeout)); err != nil {
		logger.Printf("Failed to set connection deadline: %s", err)
		return
	}

	request := make([]byte, bufferSize)
	bytesRead, err := conn.Read(request)
	if err != nil {
		logger.Printf("Failed to handle request: %s", err)
		return
	}

	requestString := strings.TrimSpace(string(request[:bytesRead]))
	if !strings.HasPrefix(requestString, "GET ") {
		logger.Printf("Invalid request: %s", requestString)
		return
	}
	filename := strings.TrimSpace(requestString[4:])
	if filepath.IsAbs(filename) || strings.Contains(filename, "..") || strings.Contains(filename, "\\") {
		logger.Printf("Invalid filename: %s", filename)
		return
	}

	fullPath := filepath.Join(rootDir, filename)
	if _, err := os.Stat(fullPath); err != nil {
		logger.Printf("Failed to access file: %s", err)
		return
	}

	serveFile(conn, fullPath, bufferSize)
}
func main() {
	rootDir := "./files"
	port := 8000
	numConnections := DefaultConcurrentConnections
	bufferSize := DefaultBufferSize
	// Create a context for cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a logger for errors.
	errLogFile, err := os.OpenFile("errors.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create errors log file: %s\n", err)
		os.Exit(1)
	}
	defer errLogFile.Close()

	logger := log.New(io.MultiWriter(os.Stderr, errLogFile), "", log.LstdFlags)

	// Start the server.
	go func() {
		logger.Printf("Starting server on port %d...", port)
		startServer(ctx, rootDir, port, numConnections, bufferSize)
	}()

	// Wait for a signal to cancel the server.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	<-ch

	logger.Printf("Shutting down server...")

	// Cancel the context to stop the server.
	cancel()

	// Wait for the server to stop.
	time.Sleep(1 * time.Second)
}
