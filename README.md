# smb-tool

CLI tool for seraching {placeholder} logs / files stored in an SMB server.

## Pre-requisites

1. Ensure you have ```make``` installed
2. Ensure you have a ```go``` binary installed

## Initialize the module
```
go mod init smb_tool
```

## Install dependencies
```
make deps
```

## Troubleshooting

This command will delete all downloaded module data from your local Go module cache, forcing a fresh download.
```
go clean -modcache
```

Remove the existing go.mod and go.sum files from your project directory. This ensures a completely fresh module initialization.
```
rm go.mod go.sum
```

This will create new go.mod and go.sum files and re-initialize the go module.
```
go mod init smb_tool
```

This step will re-download all necessary packages
```
make deps
```