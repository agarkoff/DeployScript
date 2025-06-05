# Project Structure

The deploy tool is organized into the following modular structure:

```
deploy/
├── main.go             # Main orchestration module
├── go.mod              # Go module definition
├── go.sum              # Go dependencies (auto-generated)
├── deploy.yaml         # Configuration file
├── README.md           # Project documentation
├── CONFIG_FORMAT.md    # Configuration format documentation
│
├── config/             # Configuration handling module
│   └── config.go
│
├── git/                # Git operations module
│   └── git.go
│
├── maven/              # Maven operations module
│   └── maven.go
│
└── gitlab/             # GitLab operations module
    └── gitlab.go
```

## Module Responsibilities

### main.go
- Orchestrates the entire deployment process
- Handles command-line arguments
- Coordinates calls to other modules
- Manages the deployment phases

### config/config.go
- Reads and parses YAML configuration
- Defines configuration structures
- Validates configuration data

### git/git.go
- All Git operations:
  - Status checking
  - Branch management
  - Committing and tagging
  - Pushing changes
  - Repository cleaning

### maven/maven.go
- Maven build operations
- POM file updates
- Maven cache management
- Build execution

### gitlab/gitlab.go
- GitLab API interactions
- Pipeline creation
- Pipeline monitoring
- Variable management

## Building the Project

1. Ensure all files are in the correct directories as shown above.

2. Initialize the Go module (if not already done):
```bash
go mod init deploy
```

3. Download dependencies:
```bash
go mod download
```

4. Build the project:
```bash
go build -o deploy
```

## Import Notes

The main.go file imports the modules using the module path:
```go
import (
    "deploy/config"
    "deploy/git"
    "deploy/gitlab"
    "deploy/maven"
)
```

This assumes the module name in go.mod is `deploy`.

## Testing Individual Modules

Each module can be tested independently:

```bash
# Test git module
go test ./git

# Test maven module
go test ./maven

# Test gitlab module
go test ./gitlab

# Test config module
go test ./config
```

## Adding New Functionality

To add new features:

1. Identify which module the feature belongs to
2. Add the function to the appropriate module
3. Update main.go to use the new functionality
4. Keep modules focused on their specific responsibilities

## Module Dependencies

- **config**: No dependencies on other modules
- **git**: No dependencies on other modules
- **maven**: No dependencies on other modules
- **gitlab**: No dependencies on other modules
- **main**: Depends on all other modules

This ensures a clean architecture with no circular dependencies.