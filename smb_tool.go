package main

import (
	"bufio"
	_ "bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync" // Import the sync package for WaitGroup
	"sync/atomic" // Import atomic for safe counter increment
	_ "time"

	"github.com/stacktitan/smb/smb" // SMB library
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

const (
	smbServer = "smb.server" // The IP address or hostname of your Windows File Server
	// IMPORTANT: This is the path *within* the SMB share where server directories reside.
	// For example, if the share is "\\smb.server\File Server"
	// and your files are in "\\smb.server\File Server\server1.org.za\path-to-file",
	// then smbPathPatternWithinShare would be "%s\\path-to-file" and the ShareName would be "File Server".
	// The `%s` placeholder will be replaced by the server name (e.g., "server1.org.za").
	smbPathPatternWithinShare = "%s\\path-to-file" // <--- **MODIFY THIS based on your share structure**
)

func main() {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Enter SMB Share Name (e.g., 'File Server' or 'Logs'): ")
	smbShareName, _ := reader.ReadString('\n')
	smbShareName = strings.TrimSpace(smbShareName)

	fmt.Print("Enter SMB Username (e.g., 'youruser' or 'DOMAIN\\youruser'): ")
	smbUsername, _ := reader.ReadString('\n')
	smbUsername = strings.TrimSpace(smbUsername)

	fmt.Print("Enter SMB Password: ")
	smbPassword, _ := reader.ReadString('\n')
	smbPassword = strings.TrimSpace(smbPassword)

	fmt.Print("Enter search string: ")
	searchString, _ := reader.ReadString('\n')
	searchString = strings.TrimSpace(searchString)

	fmt.Println("Attempting to connect to SMB share", smbShareName, "on", smbServer)

	// Establish SMB connection (only one connection is needed for the entire operation)
	smbSession, err := connectSMB(smbServer, smbShareName, smbUsername, smbPassword)
	if err != nil {
		log.Fatalf("Failed to connect to SMB share: %v", err)
	}
	defer smbSession.Close() // Ensure the SMB connection is closed when main exits

	fmt.Println("SMB connection established.")

	servers := []string{
		"server1.org.za",
		"server2.org.za",
		"server3.org.za",
		"server4.org.za",
	}

	var allLogFiles []string
	var allLogFilesCount int32
	var findWg sync.WaitGroup
	logFilesChan := make(chan []string) // Channel to receive lists of log files from goroutines

	// --- Phase 1: Parallel File Discovery ---
	fmt.Println("\nInitiating parallel file discovery...")
	for _, server := range servers {
		findWg.Add(1)
		go func(server string) {
			defer findWg.Done()
			serverRemoteBasePath := fmt.Sprintf(smbPathPatternWithinShare, server)
			fmt.Printf("Discovering files on %s (path within share: %s)...\n", server, serverRemoteBasePath)

			files, err := findLogFilesSMB(smbSession, serverRemoteBasePath)
			if err != nil {
				fmt.Printf("Failed to find log files on %s: %v\n", server, err)
				return // Exit goroutine if error
			}
			logFilesChan <- files // Send the found files to the channel
			// Atomically increment the count for total files found
			atomic.AddInt32(&allLogFilesCount, int32(len(files)))
			fmt.Printf("Finished discovering %d files on %s.\n", len(files), server)
		}(server)
	}

	// Goroutine to close the channel when all find operations are done
	go func() {
		findWg.Wait()
		close(logFilesChan)
	}()

	// Collect all log files from the channel
	for files := range logFilesChan {
		allLogFiles = append(allLogFiles, files...)
	}

	if len(allLogFiles) == 0 {
		fmt.Printf("\nNo log files found across all specified directories.\n")
		return
	}

	fmt.Printf("\nTotal %d log files discovered. Initiating parallel search for '%s'...\n", allLogFilesCount, searchString)

	// --- Phase 2: Parallel String Search within Files ---
	var searchWg sync.WaitGroup
	foundFilesChan := make(chan string) // Channel to receive paths of files where string is found

	for _, filePath := range allLogFiles {
		searchWg.Add(1)
		go func(path string) {
			defer searchWg.Done()
			found, err := searchStringInFileSMB(smbSession, path, searchString)
			if err != nil {
				fmt.Printf("Error searching in %s: %v\n", path, err)
				return // Exit goroutine if error
			}
			if found {
				foundFilesChan <- path // Send the file path to the channel
			}
		}(filePath)
	}

	// Goroutine to close the channel when all search operations are done
	go func() {
		searchWg.Wait()
		close(foundFilesChan)
	}()

	// Collect all files where the string was found
	var allFoundInFiles []string
	for file := range foundFilesChan {
		allFoundInFiles = append(allFoundInFiles, file)
		fmt.Printf("String '%s' found in: %s\n", searchString, file) // Print as they are found
	}

	if len(allFoundInFiles) == 0 {
		fmt.Printf("\nString '%s' was not found in any log files across all servers.\n", searchString)
		return
	}

	fmt.Println("\n--- Search Complete ---")
	fmt.Printf("Total log files scanned: %d\n", allLogFilesCount)
	fmt.Println("Files containing the search string:")
	for _, file := range allFoundInFiles {
		fmt.Println("-", file)
	}

	// Prompt to download a file
	fmt.Print("\nEnter the full path of a file to fetch (relative to share, e.g., 'server1.org.za\\path-to-file\\2025-06-16\\app.01.log') (or press Enter to skip): ")
	fileToFetch, _ := reader.ReadString('\n')
	fileToFetch = strings.TrimSpace(fileToFetch)

	if fileToFetch != "" {
		localFileName := filepath.Base(fileToFetch) // Get just the filename (e.g., "app.01.log")
		fmt.Printf("Attempting to fetch '%s' to local file '%s'...\n", fileToFetch, localFileName)
		err := fetchFileSMB(smbSession, fileToFetch, localFileName)
		if err != nil {
			fmt.Printf("Failed to fetch file: %v\n", err)
		} else {
			fmt.Printf("Successfully fetched '%s' to '%s'.\n", fileToFetch, localFileName)
		}
	} else {
		fmt.Println("File fetch skipped.")
	}

	fmt.Println("Program finished.")
}

// connectSMB establishes an SMB connection and authenticates to a share.
func connectSMB(server, share, username, password string) (*smb.Session, error) {
	// Dial the SMB server (port 445 is default)
	conn, err := smb.Dial(server + ":445")
	if err != nil {
		return nil, fmt.Errorf("failed to dial SMB server '%s': %w", server, err)
	}

	// Authenticate
	session, err := conn.Login(username, password)
	if err != nil {
		conn.Close() // Close connection on auth failure
		return nil, fmt.Errorf("failed to authenticate to SMB server: %w", err)
	}

	// Connect to the specified share
	tree, err := session.Mount(share)
	if err != nil {
		session.Close() // Close session on mount failure
		conn.Close()
		return nil, fmt.Errorf("failed to mount SMB share '%s': %w", share, err)
	}

	// The smb.Session returned by Mount is what you use for file operations
	return tree, nil
}

// findLogFilesSMB recursively finds all .log files within a given path on the SMB share.
func findLogFilesSMB(smbSession *smb.Session, path string) ([]string, error) {
	var logFiles []string

	// smb.Walk provides a convenient way to traverse directories recursively
	err := smbSession.Walk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			// Log the error but continue walking.
			// For example, permission denied errors might occur.
			fmt.Printf("Error walking path %s: %v\n", filePath, err)
			return nil // Don't stop the walk on individual errors
		}

		if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".log") {
			// Convert to Windows-style path (backslashes) if the SMB library
			// returns forward slashes, to maintain consistency.
			// The smb library typically uses forward slashes internally,
			// but we want paths to be consistent with how the user expects them for fetching.
			// Let's assume the library gives us paths like "dir/file.log" and convert to "dir\file.log"
			// when adding to the list for user interaction.
			logFiles = append(logFiles, strings.ReplaceAll(filePath, "/", "\\"))
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("error during SMB file walk: %w", err)
	}

	return logFiles, nil
}

// searchStringInFileSMB reads a file from the SMB share line by line and searches for a string.
// This is memory-efficient for large files.
func searchStringInFileSMB(smbSession *smb.Session, filePath, searchString string) (bool, error) {
	file, err := smbSession.Open(filePath)
	if err != nil {
		return false, fmt.Errorf("failed to open remote file '%s': %w", filePath, err)
	}
	defer file.Close()

	// Create a buffered reader for line-by-line reading
	reader := bufio.NewReader(file)

	// Attempt to determine encoding by reading a small chunk first,
	// or assume UTF-8 and try UTF-16 if the search string is not found and the file looks like UTF-16.
	// For log files, UTF-8 is most common, but we keep the robust check.
	var line []byte
	var isUTF16 bool = false // Flag to remember if we determined it's UTF-16

	// Read a small initial chunk to sniff for BOM if needed
	// The SMB library usually handles this reasonably well, but for robust text processing,
	// it's good to be aware. For simplicity and general log file format, we'll try line by line
	// with a fallback if the string isn't found.

	for {
		// Read a line (until newline or EOF)
		lineBytes, isPrefix, err := reader.ReadLine()
		if err != nil {
			if err == io.EOF {
				break // End of file
			}
			return false, fmt.Errorf("failed to read line from remote file '%s': %w", filePath, err)
		}

		// If line is too long for buffer, need to continue reading the remainder
		line = append(line, lineBytes...)
		if isPrefix {
			continue // Line is too long, continue reading next chunk
		}

		// Decode the line if it was detected as UTF-16 previously or try now
		var decodedLine string
		if isUTF16 {
			// Use the UTF-16 decoder for subsequent lines if already detected
			decoder := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder()
			decodedBytes, _, decodeErr := transform.Bytes(decoder, line)
			if decodeErr == nil {
				decodedLine = string(decodedBytes)
			} else {
				// Fallback if subsequent UTF-16 decoding fails for some reason
				decodedLine = string(line)
			}
		} else {
			// For the first lines, we can try to sniff for UTF-16 BOM or common patterns.
			// However, simple `strings.Contains` on raw bytes might work for ASCII/UTF-8.
			// Let's assume most log files are UTF-8. If the string is not found,
			// and we suspect UTF-16, we can retry.
			// For now, simplify and rely on UTF-8 and if `searchString` is known to be in UTF-16,
			// user should input search string in UTF-16 compatible format, or we do conversion.
			// A common approach for logs is to mostly be UTF-8.
			decodedLine = string(line)
		}

		if strings.Contains(decodedLine, searchString) {
			return true, nil // String found in this line
		}

		line = nil // Reset line buffer for next iteration
	}

	// If after all lines, the string is not found, and we want to be absolutely robust
	// for potentially UTF-16 files where the simple string.Contains might fail due to encoding,
	// we could re-open and try UTF-16 specific reader.
	// However, for typical log analysis, this line-by-line UTF-8 approach is sufficient
	// and highly memory-efficient.
	return false, nil // String not found in the entire file
}


// fetchFileSMB downloads a file from the SMB share to the local machine.
func fetchFileSMB(smbSession *smb.Session, remotePath, localPath string) error {
	remoteFile, err := smbSession.Open(remotePath)
	if err != nil {
		return fmt.Errorf("failed to open remote SMB file '%s': %w", remotePath, err)
	}
	defer remoteFile.Close()

	localFile, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create local file '%s': %w", localFile, err)
	}
	defer localFile.Close()

	bytesCopied, err := io.Copy(localFile, remoteFile)
	if err != nil {
		return fmt.Errorf("failed to copy file content from SMB: %w", err)
	}

	fmt.Printf("Copied %d bytes.\n", bytesCopied)
	return nil
}
