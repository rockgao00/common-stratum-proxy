package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
)

var (
	configPath = flag.String("config", "config.json", "Path to JSON configuration file")
)

type MinerConfig struct {
	Auth     string `json:"auth"`
	Pass     string `json:"pass"`
	Ipenable bool   `json:"ipenable"`
}

type Config struct {
	Listen  string      `json:"listen"`
	Targets []string    `json:"targets"`
	Miner   MinerConfig `json:"miner"`
}

func getClientIP(conn net.Conn) string {
	clientAddr := conn.RemoteAddr().String()
	clientIP, _, err := net.SplitHostPort(clientAddr)
	if err != nil {
		log.Println("Error parsing client address:", err)
		return ""
	}
	formattedIP := strings.ReplaceAll(clientIP, ".", "x")
	return formattedIP
}

func ModifyJSON(data string, config *Config, ip string) string {
	var jsonData map[string]interface{}
	err := json.Unmarshal([]byte(data), &jsonData)
	if err != nil {
		log.Printf("Error unmarshalling JSON: %v", err)
		return data
	}

	if method, ok := jsonData["method"]; ok {
		switch method {
		case "mining.authorize":
			if params1, ok := jsonData["params"].([]interface{}); ok {
				if false == config.Miner.Ipenable {
					params1[0] = config.Miner.Auth
				} else {
					params1[0] = config.Miner.Auth + ip
				}
				jsonData["params"] = params1
			}
		case "mining.submit":
			if params2, ok := jsonData["params"].([]interface{}); ok {
				if false == config.Miner.Ipenable {
					params2[0] = config.Miner.Auth
				} else {
					params2[0] = config.Miner.Auth + ip
				}
				jsonData["params"] = params2
			}
		default:
		}

		modifiedData, err := json.Marshal(jsonData)
		if err != nil {
			log.Printf("Error marshalling JSON: %v", err)
			return data
		}
		return string(modifiedData)
	}

	return data
}

func HandleClient(clientConn net.Conn, config *Config, wg *sync.WaitGroup) {
	defer wg.Done()
	defer clientConn.Close()

	remoteConn, err := net.Dial("tcp", config.Targets[0])
	if err != nil {
		log.Printf("Failed to connect to remote server 0: %v", err)
		remoteConn, err = net.Dial("tcp", config.Targets[1])
		if err != nil {
			log.Printf("Failed to connect to remote server 1: %v", err)
			return
		}
	}

	defer remoteConn.Close()

	clientReader := bufio.NewReader(clientConn)
	remoteReader := bufio.NewReader(remoteConn)

	var clientWg sync.WaitGroup
	clientWg.Add(2)

	go func() {
		defer clientWg.Done()
		for {
			clientData, err := clientReader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					log.Printf("Error reading from client: %v", err)
				}
				break
			}

			modifiedData := ModifyJSON(strings.TrimSpace(clientData), config, getClientIP(clientConn))
			_, err = remoteConn.Write([]byte(modifiedData + "\n"))
			if err != nil {
				log.Printf("Error writing to remote server: %v", err)
				break
			}
		}
	}()

	go func() {
		defer clientWg.Done()
		for {
			remoteData, err := remoteReader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					log.Printf("Error reading from remote server: %v", err)
				}
				break
			}
			_, err = clientConn.Write([]byte(remoteData))
			if err != nil {
				log.Printf("Error writing to client: %v", err)
				break
			}
		}
	}()

	clientWg.Wait()
}

func StartProxy(config *Config) {
	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		log.Fatalf("Failed to start proxy server: %v", err)
	}
	defer listener.Close()

	log.Printf("Listening on %s", config.Listen)

	var wg sync.WaitGroup
	// Channel to receive OS signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	// Channel to notify the main goroutine to stop accepting new connections
	stopChan := make(chan struct{})

	go func() {
		for {
			clientConn, err := listener.Accept()
			if err != nil {
				select {
				case <-stopChan: // Listener closed, exit goroutine
					return
				default:
					log.Printf("Failed to accept connection: %v", err)
					continue
				}
			}

			log.Printf("Accepted connection from %s", clientConn.RemoteAddr().String())
			wg.Add(1)
			go HandleClient(clientConn, config, &wg)
		}
	}()

	<-sigChan
	close(stopChan)
	listener.Close()

	wg.Wait()
	log.Println("Proxy server stopped")
}

func loadConfig(path string) (*Config, error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config Config
	err = json.Unmarshal(file, &config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

func main() {
	logFile, err := os.OpenFile("./app.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer func(logFile *os.File) {
		err := logFile.Close()
		if err != nil {
			log.Printf("Error closing log file: %v", err)
		}
	}(logFile)

	log.SetOutput(logFile)
	flag.Parse()

	config, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	if len(config.Targets) == 0 {
		log.Fatal("No target addresses specified in config")
	}

	StartProxy(config)
}
