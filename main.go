package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	pb "github.com/kumarsgoyal/log-replica/proto"
	grpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	yaml "gopkg.in/yaml.v2"
)

const (
	defaultLogFilePath     = "/app/data/log.yaml"
	defaultCounterFilePath = "/app/data/counter.yaml"
	defaultPort            = ":8080"
	defaultServiceAddress  = "log-replica-service:8080"
	defaultReplicaCount    = 3
	defaultTimeInterval    = 1000
	defaultTimeOut         = 5000
	roleLog                = "log"
	roleReplica            = "replica"
	envHostname            = "HOSTNAME"
	envPort                = "PORT"
	envLogFilePath         = "LOG_FILE_PATH"
	envCounterFilePath     = "COUNTER_FILE_PATH"
	envReplicaCount        = "REPLICA_COUNT"
	envTimeInterval        = "TIME_INTERVAL"
	envTimeTimeout         = "TIMEOUT"
	envServiceAddress      = "SERVICE_ADDR"
)

type LogEntry struct {
	Persisted map[int]Replica `yaml:"persisted"` // Data already written to replicas
	Issued    map[int]Replica `yaml:"issued"`    // Data issued to replicas but not yet confirmed
}

type Replica struct {
	ReplicaId int   `yaml:"replicaId"` // Unique ID for the replica
	Counter   int64 `yaml:"counter"`   // Counter value for this replica
}

type ReplicaEntry struct {
	Counter int64 `yaml:"counter"`
}

type Server struct {
	pb.UnimplementedLogServiceServer
	pb.UnimplementedInitServiceServer
}

type Config struct {
	Role            string
	Port            string
	ServiceAddress  string
	LogFilePath     string
	CounterFilePath string
	TimeInterval    int
	TimeOut         int
	ReplicaCount    int
	ReplicaIndex    int
}

var (
	config           Config
	logData          LogEntry
	logDataMutex     sync.Mutex
	logFileMutex     sync.RWMutex // Mutex for log file access
	counterData      ReplicaEntry
	counterDataMutex sync.Mutex
	counterFileMutex sync.RWMutex // Mutex for counter file access
)

func main() {
	config.Role = getRole()
	log.Printf("Pod Role: %s", config.Role)

	switch config.Role {
	case roleLog:
		startLogProcess()
	case roleReplica:
		startReplicaProcess()
	default:
		log.Fatalf("Invalid role: %s. Use '%s' or '%s'.", config.Role, roleLog, roleReplica)
	}
}

func getRole() string {
	hostname := os.Getenv(envHostname)
	index := getIndex()
	if hostname == "" {
		log.Fatalf("ROLE environment variable is not set")
	}
	if index == 0 { // 0th pod is the log pod
		return roleLog
	}
	return roleReplica // All other pods are replicas
}

func startLogProcess() {
	loadLogConfig()
	go startLogServer()
	readLogFile()

	distributeCounters()
}

func startReplicaProcess() {
	loadReplicaConfig()
	readCounterFile()
	initLogReplicaData()

	go startReplicaServer()
	select {}
}

func loadLogConfig() {
	config.Port = getEnvOrDefault(envPort, defaultPort)
	config.TimeOut = getEnvOrDefaultInt(envTimeTimeout, defaultTimeOut)
	config.LogFilePath = getEnvOrDefault(envLogFilePath, defaultLogFilePath)
	config.ReplicaCount = getEnvOrDefaultInt(envReplicaCount, defaultReplicaCount)
	config.TimeInterval = getEnvOrDefaultInt(envTimeInterval, defaultTimeInterval)
	config.ServiceAddress = getEnvOrDefault(envServiceAddress, defaultServiceAddress)
}
func loadReplicaConfig() {
	config.Port = getEnvOrDefault(envPort, defaultPort)
	config.ReplicaIndex = getIndex()
	config.CounterFilePath = getCounterFilePath(getEnvOrDefault(envCounterFilePath, defaultCounterFilePath))
	config.ServiceAddress = getEnvOrDefault(envServiceAddress, defaultServiceAddress)
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvOrDefaultInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getCounterFilePath(counterPathFile string) string {
	if res := strings.Compare(counterPathFile, defaultCounterFilePath); res == 0 {
		log.Printf("The counter path is the default.")
		return counterPathFile
	}
	return fmt.Sprintf(counterPathFile, config.ReplicaIndex)
}

func getIndex() int {
	podname := os.Getenv(envHostname)
	re := regexp.MustCompile(`\d+`)

	// Find the first match of digits
	match := re.FindString(podname)
	if match != "" {
		replicaIndex, err := strconv.Atoi(match)
		if err != nil {
			log.Printf("Error converting string to int: %v", err)
			return 0
		}
		return replicaIndex
	}

	log.Printf("No digits found in %v, returning 0", podname)
	return 0
}

func startLogServer() {
	log.Printf("Starting log server on port %s...", config.Port)
	lis, err := net.Listen("tcp", config.Port)

	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterInitServiceServer(grpcServer, &Server{})
	log.Printf("Server started at %v", lis.Addr())
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve log server: %v", err)
	}

	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
		log.Println("Received shutdown signal. Shutting down gracefully...")

		// Perform any cleanup before shutdown (e.g., close connections, cleanup resources)
		// Gracefully stop the server(s)
		if config.Role == roleLog {
			grpcServer.GracefulStop()
		} else if config.Role == roleReplica {
			grpcServer.GracefulStop()
		}

		log.Println("Server shut down gracefully.")
		os.Exit(0)
	}()
}

func startReplicaServer() {
	log.Printf("Starting replica server on port %s...", config.Port)
	lis, err := net.Listen("tcp", config.Port)
	if err != nil {
		log.Fatalf("Failed to start replica server: %v", err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterLogServiceServer(grpcServer, &Server{})
	log.Printf("Replica server started on %v", config.Port)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve replica server: %v", err)
	}

	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
		log.Println("Received shutdown signal. Shutting down gracefully...")

		// Perform any cleanup before shutdown (e.g., close connections, cleanup resources)
		// Gracefully stop the server(s)
		if config.Role == roleLog {
			grpcServer.GracefulStop()
		} else if config.Role == roleReplica {
			grpcServer.GracefulStop()
		}

		log.Println("Server shut down gracefully.")
		os.Exit(0)
	}()
}

func readLogFile() error {
	logDataMutex.Lock() // Lock access to logData
	defer logDataMutex.Unlock()

	data, err := os.ReadFile(config.LogFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("Log file not found, initializing a new one.")

			logData = LogEntry{
				Issued:    make(map[int]Replica),
				Persisted: make(map[int]Replica),
			}
			return writeLogFile()
		}
		return fmt.Errorf("failed to read log file: %v", err)
	}

	if len(data) == 0 {
		log.Println("Log file is empty, initializing default data.")
		logData = LogEntry{
			Issued:    make(map[int]Replica),
			Persisted: make(map[int]Replica),
		}
		return writeLogFile()
	}

	if err := yaml.Unmarshal(data, &logData); err != nil {
		return fmt.Errorf("failed to unmarshal log data: %v", err)
	}

	log.Printf("Successfully loaded log data from %s", config.LogFilePath)
	return nil
}

func readCounterFile() error {
	counterDataMutex.Lock()
	defer counterDataMutex.Unlock()

	data, err := os.ReadFile(config.CounterFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("%v file not found, initializing a new one.", config.CounterFilePath)
			counterData = ReplicaEntry{}
			return writeCounterFile()
		}
		return fmt.Errorf("failed to read %v file: %v", config.CounterFilePath, err)
	}

	if len(data) == 0 {
		log.Printf("%v file is empty, initializing default data.", config.CounterFilePath)
		counterData = ReplicaEntry{}
		return writeLogFile()
	}

	if err := yaml.Unmarshal(data, &counterData); err != nil {
		return fmt.Errorf("failed to unmarshal counter data: %v", err)
	}

	log.Printf("Successfully loaded counter data from %v", config.CounterFilePath)
	return nil
}

func writeLogFile() error {

	logFileMutex.RLock() // Lock file write access
	defer logFileMutex.RUnlock()

	logDataMutex.Lock()
	defer logDataMutex.Unlock()

	data, err := yaml.Marshal(&logData)
	if err != nil {
		return fmt.Errorf("failed to marshal log data: %v", err)
	}

	return os.WriteFile(config.LogFilePath, data, 0644)
}

func writeCounterFile() error {
	counterFileMutex.RLock() // Lock counter file write access
	defer counterFileMutex.RUnlock()

	counterDataMutex.Lock()
	defer counterDataMutex.Unlock()

	data, err := yaml.Marshal(&counterData)
	if err != nil {
		return fmt.Errorf("failed to marshal counter data: %v", err)
	}

	return os.WriteFile(config.CounterFilePath, data, 0644)
}

func initLogReplicaData() {
	logAddress := fmt.Sprintf(config.ServiceAddress, 0)
	persistedCounter, issuedCounter, err := callCheckInitState(logAddress)
	if err != nil {
		log.Printf("Error calling CheckInitState on log server %v: %v", logAddress, err)
		return
	}
	// Check if the issued and persisted counters are consistent for the replica
	checkReplicaConsistency(persistedCounter, issuedCounter)
}

func callCheckInitState(logAddress string) (int64, int64, error) {
	// Establish a gRPC connection to the log server
	conn, err := grpc.NewClient(logAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return 0, 0, fmt.Errorf("failed to connect to log at %s: %v", logAddress, err)
	}
	defer conn.Close()

	client := pb.NewInitServiceClient(conn)

	req := &pb.InitRequest{
		Index: int64(config.ReplicaIndex),
	}
	resp, err := client.CheckInitState(context.Background(), req)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to call CheckInitState on loh at %s: %v", logAddress, err)
	}

	log.Printf("Fetched stored counters from log at %s: %d, %d", logAddress, resp.PersistedCounter, resp.IssuedCounter)
	return resp.PersistedCounter, resp.IssuedCounter, nil
}

func checkReplicaConsistency(persistedCounter int64, issuedCounter int64) {
	replicaCounter := counterData.Counter
	replicaID := config.ReplicaIndex

	log.Printf("IssuedCounter: %d, PersistedCounter: %d, ReplicaCounterData: %d for replicaId: %d", issuedCounter, persistedCounter, replicaCounter, replicaID)

	// Check for consistency
	if replicaCounter != issuedCounter && replicaCounter != persistedCounter {
		log.Printf("Inconsistent counters detected on replica %d", replicaID)
		log.Fatalf("IssuedCounter: %d, PersistedCounter: %d, ReplicaCounterData: %d", issuedCounter, persistedCounter, replicaCounter)
	}
}

func distributeCounters() {
	for {
		var wg sync.WaitGroup

		for i := 1; i < config.ReplicaCount; i++ {
			wg.Add(1)
			go func(replicaIndex int) {
				defer wg.Done()
				serviceAddress := fmt.Sprintf(config.ServiceAddress, replicaIndex)
				sendCounterToReplica(serviceAddress, replicaIndex)
			}(i)
		}

		wg.Wait()
		time.Sleep(time.Duration(config.TimeInterval) * time.Millisecond)
	}
}

func sendCounterToReplica(replicaAddress string, replicaIndex int) {
	issuedCounter := generateNewCounter(replicaIndex)

	logDataMutex.Lock()
	logData.Issued[replicaIndex] = Replica{
		ReplicaId: replicaIndex,
		Counter:   issuedCounter,
	}
	logDataMutex.Unlock()

	log.Printf("Storing issued counter %d for replica %d", issuedCounter, replicaIndex)
	// Save the issued counter to log.yaml
	if err := writeLogFile(); err != nil {
		log.Printf("Error storing issued counter for replica %d: %v", replicaIndex, err)
		return
	}

	// Send the issued counter to the replica via gRPC
	conn, err := grpc.NewClient(replicaAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("Error connecting to replica %d: %v", replicaIndex, err)
	}
	defer conn.Close()

	log.Printf("Sending issued counter %d for replica %d", issuedCounter, replicaIndex)
	client := pb.NewLogServiceClient(conn)
	req := &pb.ReplicaRequest{IssuedCounter: issuedCounter}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.TimeOut)*time.Second)
	defer cancel()

	resp, err := client.IssueCounter(ctx, req)
	if err != nil {
		log.Printf("Error sending counter to replica %d: %v", replicaIndex, err)
		return
	}

	persistedCounter := resp.StoredCounter

	logDataMutex.Lock()
	logData.Persisted[replicaIndex] = Replica{
		ReplicaId: replicaIndex,
		Counter:   persistedCounter,
	}
	logDataMutex.Unlock()

	log.Printf("Storing persisted counter %d for replica %d", persistedCounter, replicaIndex)
	if err := writeLogFile(); err != nil {
		log.Printf("Error saving persisted counter for replica %d: %v", replicaIndex, err)
	}
}

func generateNewCounter(replicaIndex int) int64 {
	return time.Now().Unix() + int64(replicaIndex)
}

func (s *Server) CheckInitState(ctx context.Context, req *pb.InitRequest) (*pb.InitResponse, error) {
	replicaIndex := int(req.Index)
	log.Printf("Received CheckInitState request from replica %d", replicaIndex)

	logDataMutex.Lock()
	defer logDataMutex.Unlock()

	// Fetch the persisted data for the given index
	persistedData, exists := logData.Persisted[replicaIndex]
	if !exists {
		log.Printf("No persisted log data found for replica %d. Returning default value.", replicaIndex)
		persistedData = Replica{}
	}
	issuedData, exists := logData.Issued[replicaIndex]
	if !exists {
		log.Printf("No persisted log data found for replica %d. Returning default value.", replicaIndex)
		issuedData = Replica{}
	}

	log.Printf("Returning persisted log data %d and issued log data %d for replica %d", persistedData.Counter, issuedData.Counter, replicaIndex)

	return &pb.InitResponse{
		PersistedCounter: persistedData.Counter,
		IssuedCounter:    issuedData.Counter,
	}, nil
}

func (s *Server) IssueCounter(ctx context.Context, req *pb.ReplicaRequest) (*pb.ReplicaResponse, error) {
	// Store the counter received from the log server
	log.Printf("Received issued counter: %v", req.IssuedCounter)

	storedCounter := req.IssuedCounter
	err := overwriteCounterInFile(storedCounter)
	if err != nil {
		return nil, fmt.Errorf("failed to store counter: %v", err)
	}

	// Respond back with the stored counter value
	return &pb.ReplicaResponse{
		StoredCounter: storedCounter,
	}, nil
}

func overwriteCounterInFile(counter int64) error {
	counterDataMutex.Lock()
	defer counterDataMutex.Unlock()

	counterData.Counter = counter

	counterFileMutex.Lock() // Lock counter file write access
	defer counterFileMutex.Unlock()

	updatedData, err := yaml.Marshal(&counterData)
	if err != nil {
		return fmt.Errorf("failed to marshal updated counter data: %v", err)
	}

	// Write the updated counter data back to the file (this will overwrite the existing content)
	err = os.WriteFile(config.CounterFilePath, updatedData, 0644)
	if err != nil {
		return fmt.Errorf("failed to write updated counter data to file: %v", err)
	}

	log.Printf("Stored counter %v in %v", counter, config.CounterFilePath)
	return nil
}
