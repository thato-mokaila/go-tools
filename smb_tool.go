package main

import (
	"bufio"
	"fmt"
	"io"
	iofs "io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	_ "time"

	"github.com/hirochachacha/go-smb2"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

const (
	smbServer                 = "smb.server"
	smbPathPatternWithinShare = "%s\\path-to-file"
)

func main() {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Enter SMB Share Name: ")
	smbShareName, _ := reader.ReadString('\n')
	smbShareName = strings.TrimSpace(smbShareName)

	fmt.Print("Enter SMB Username: ")
	smbUsername, _ := reader.ReadString('\n')
	smbUsername = strings.TrimSpace(smbUsername)

	fmt.Print("Enter SMB Password: ")
	smbPassword, _ := reader.ReadString('\n')
	smbPassword = strings.TrimSpace(smbPassword)

	fmt.Print("Enter search string: ")
	searchString, _ := reader.ReadString('\n')
	searchString = strings.TrimSpace(searchString)

	fmt.Println("Attempting to connect to SMB share", smbShareName, "on", smbServer)

	smbShareFS, closeFunc, err := connectSMB(smbServer, smbShareName, smbUsername, smbPassword)
	if err != nil {
		log.Fatalf("Failed to connect to SMB share: %v", err)
	}
	defer closeFunc()

	fmt.Println("SMB connection established.")

	servers := []string{
		"server1.igroup.io",
		"server2.igroup.io",
		"server3.igroup.io",
		"server4.igroup.io",
		"server5.igroup.io",
		"server6.igroup.io",
	}

	var allLogFiles []string
	var allLogFilesCount int32
	var findWg sync.WaitGroup
	logFilesChan := make(chan []string)

	fmt.Println("\nInitiating parallel file discovery...")
	for _, server := range servers {
		findWg.Add(1)
		go func(server string) {
			defer findWg.Done()
			serverPath := fmt.Sprintf(smbPathPatternWithinShare, server)
			fmt.Printf("Discovering files on %s (path: %s)...\n", server, serverPath)

			files, err := findLogFilesSMB(smbShareFS, serverPath)
			if err != nil {
				fmt.Printf("Failed to find log files on %s: %v\n", server, err)
				return
			}
			logFilesChan <- files
			atomic.AddInt32(&allLogFilesCount, int32(len(files)))
			fmt.Printf("Finished discovering %d files on %s.\n", len(files), server)
		}(server)
	}

	go func() {
		findWg.Wait()
		close(logFilesChan)
	}()

	for files := range logFilesChan {
		allLogFiles = append(allLogFiles, files...)
	}

	if len(allLogFiles) == 0 {
		fmt.Printf("\nNo log files found.\n")
		return
	}

	fmt.Printf("\nTotal %d log files discovered. Searching for '%s'...\n", allLogFilesCount, searchString)

	var searchWg sync.WaitGroup
	foundFilesChan := make(chan string)

	for _, filePath := range allLogFiles {
		searchWg.Add(1)
		go func(path string) {
			defer searchWg.Done()
			found, err := searchStringInFileSMB(smbShareFS, path, searchString)
			if err != nil {
				fmt.Printf("Error searching in %s: %v\n", path, err)
				return
			}
			if found {
				foundFilesChan <- path
			}
		}(filePath)
	}

	go func() {
		searchWg.Wait()
		close(foundFilesChan)
	}()

	var allFoundInFiles []string
	for file := range foundFilesChan {
		allFoundInFiles = append(allFoundInFiles, file)
		fmt.Printf("Found: %s\n", file)
	}

	if len(allFoundInFiles) == 0 {
		fmt.Printf("\nNo matches for '%s'.\n", searchString)
		return
	}

	fmt.Println("\n--- Search Complete ---")
	fmt.Printf("Total scanned: %d\n", allLogFilesCount)
	fmt.Println("Matched files:")
	for _, file := range allFoundInFiles {
		fmt.Println("-", file)
	}

	fmt.Print("\nEnter file path to fetch (or press Enter to skip): ")
	fileToFetch, _ := reader.ReadString('\n')
	fileToFetch = strings.TrimSpace(fileToFetch)

	if fileToFetch != "" {
		localFileName := filepath.Base(fileToFetch)
		fmt.Printf("Fetching '%s' to '%s'...\n", fileToFetch, localFileName)
		err := fetchFileSMB(smbShareFS, fileToFetch, localFileName)
		if err != nil {
			fmt.Printf("Failed to fetch: %v\n", err)
		} else {
			fmt.Printf("Fetched to '%s'.\n", localFileName)
		}
	} else {
		fmt.Println("Fetch skipped.")
	}

	fmt.Println("Program complete.")
}

func connectSMB(server, share, username, password string) (*smb2.Share, func(), error) {
	conn, err := net.Dial("tcp", server+":445")
	if err != nil {
		return nil, nil, fmt.Errorf("TCP dial failed: %w", err)
	}

	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     username,
			Password: password,
		},
	}

	s, err := d.Dial(conn)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("SMB dial failed: %w", err)
	}

	fs, err := s.Mount(share)
	if err != nil {
		s.Logoff()
		conn.Close()
		return nil, nil, fmt.Errorf("SMB mount failed: %w", err)
	}

	closeFunc := func() {
		fs.Umount()
		s.Logoff()
		conn.Close()
	}

	return fs, closeFunc, nil
}

func findLogFilesSMB(smbShareFS *smb2.Share, path string) ([]string, error) {
	var logFiles []string

	err := iofs.WalkDir(smbShareFS.DirFS("."), path, func(entryPath string, d iofs.DirEntry, err error) error {
		if err != nil {
			fmt.Printf("Walk error: %v\n", err)
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".log") {
			logFiles = append(logFiles, strings.ReplaceAll(entryPath, "/", "\\"))
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("walk failed: %w", err)
	}
	return logFiles, nil
}

func searchStringInFileSMB(smbShareFS *smb2.Share, filePath, searchString string) (bool, error) {
	filePath = strings.ReplaceAll(filePath, "\\", "/") // <-- SMB expects forward slashes
	file, err := smbShareFS.Open(filePath)
	if err != nil {
		return false, fmt.Errorf("open failed: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)

	for {
		lineBytes, isPrefix, err := reader.ReadLine()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false, fmt.Errorf("read error: %w", err)
		}

		line := make([]byte, len(lineBytes))
		copy(line, lineBytes)
		for isPrefix {
			moreBytes, moreIsPrefix, err := reader.ReadLine()
			if err == io.EOF {
				break
			}
			if err != nil {
				return false, fmt.Errorf("read continuation error: %w", err)
			}
			line = append(line, moreBytes...)
			isPrefix = moreIsPrefix
		}

		var decodedLine string
		if len(line) > 1 && line[0] == 0xFF && line[1] == 0xFE {
			decoder := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder()
			decodedBytes, _, decodeErr := transform.Bytes(decoder, line)
			if decodeErr == nil {
				decodedLine = string(decodedBytes)
			} else {
				decodedLine = string(line)
			}
		} else if len(line) > 1 && line[0] == 0xFE && line[1] == 0xFF {
			decoder := unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewDecoder()
			decodedBytes, _, decodeErr := transform.Bytes(decoder, line)
			if decodeErr == nil {
				decodedLine = string(decodedBytes)
			} else {
				decodedLine = string(line)
			}
		} else {
			decodedLine = string(line)
		}

		if strings.Contains(decodedLine, searchString) {
			return true, nil
		}
	}

	return false, nil
}

func fetchFileSMB(smbShareFS *smb2.Share, remotePath, localPath string) error {
	remotePath = strings.ReplaceAll(remotePath, "\\", "/") // <-- Normalize for Open
	file, err := smbShareFS.Open(remotePath)
	if err != nil {
		return fmt.Errorf("remote open failed: %w", err)
	}
	defer file.Close()

	localFile, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("local create failed: %w", err)
	}
	defer localFile.Close()

	n, err := io.Copy(localFile, file)
	if err != nil {
		return fmt.Errorf("copy failed: %w", err)
	}

	fmt.Printf("Copied %d bytes.\n", n)
	return nil
}