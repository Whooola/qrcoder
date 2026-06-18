@echo off
echo Building QRCoder...

REM Generate icon resource
if exist assets\icon.ico (
    rsrc -ico assets\icon.ico -o rsrc.syso
    if %errorlevel% neq 0 (
        echo Warning: rsrc failed, building without icon
    )
)

REM Build for Windows
set CGO_ENABLED=1
set GOOS=windows
set GOARCH=amd64

go build -ldflags "-s -w -H windowsgui" -o qrcoder.exe

if %errorlevel% equ 0 (
    echo Build complete: qrcoder.exe
) else (
    echo Build failed!
    exit /b 1
)
