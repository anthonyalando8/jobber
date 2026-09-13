@echo off
REM Prospector setup script for Windows (no Make required)
setlocal enabledelayedexpansion

set COMMAND=%1

if "%COMMAND%"=="" (
    echo Prospector — Windows Setup
    echo Usage: setup.bat [command]
    echo.
    echo Commands:
    echo   setup.bat download   - Download Go dependencies
    echo   setup.bat db         - Create and migrate database
    echo   setup.bat run        - Start the server
    echo   setup.bat test       - Run basic checks
    goto :eof
)

if "%COMMAND%"=="download" (
    echo Downloading Go dependencies...
    go mod download
    if errorlevel 1 exit /b 1
    echo Dependencies downloaded successfully
    goto :eof
)

if "%COMMAND%"=="db" (
    echo Create the database named by DATABASE_URL first. Existing databases require inspect and verified backup.
    go run ./cmd/prospector-migrate -command apply
    exit /b !errorlevel!
)

if "%COMMAND%"=="migrate" (
    go run ./cmd/prospector-migrate -command apply -proof "%~2"
    exit /b !errorlevel!
)

if "%COMMAND%"=="inspect" (
    go run ./cmd/prospector-migrate -command inspect
    exit /b !errorlevel!
)

if "%COMMAND%"=="unit-test" (
    go test ./...
    exit /b !errorlevel!
    goto :eof
)

if "%COMMAND%"=="run" (
    echo Starting Prospector on http://localhost:8080
    echo Press Ctrl+C to stop
    echo.
    go run ./cmd/prospector
    goto :eof
)

if "%COMMAND%"=="test" (
    echo Running basic checks...
    echo.
    echo 1. Checking Go version...
    go version
    echo.
    echo 2. Building project...
    go build -o bin/prospector.exe ./cmd/prospector
    if errorlevel 1 (
        echo Build failed
        exit /b 1
    )
    echo Build successful!
    echo.
    echo Next steps:
    echo   - Copy .env.example to .env and edit with your API keys
    echo   - Run: setup.bat db
    echo   - Run: setup.bat run
    goto :eof
)

echo Unknown command: %COMMAND%
exit /b 1
