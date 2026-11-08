@echo off
setlocal EnableDelayedExpansion

set "SCRIPT_DIR=%~dp0"
for %%I in ("%SCRIPT_DIR%..\..\..") do set "REPO_ROOT=%%~fI"

if exist "%REPO_ROOT%\gdocumentor.exe" (
    "%REPO_ROOT%\gdocumentor.exe" %*
    exit /b %ERRORLEVEL%
)

where gdocumentor >nul 2>nul
if %ERRORLEVEL% EQU 0 (
    gdocumentor %*
    exit /b %ERRORLEVEL%
)

if exist "%USERPROFILE%\.local\bin\gdocumentor.exe" (
    "%USERPROFILE%\.local\bin\gdocumentor.exe" %*
    exit /b %ERRORLEVEL%
)

if exist "%USERPROFILE%\go\bin\gdocumentor.exe" (
    "%USERPROFILE%\go\bin\gdocumentor.exe" %*
    exit /b %ERRORLEVEL%
)

if "%PROCESSOR_ARCHITECTURE%"=="ARM64" (
    if exist "%REPO_ROOT%\bin\gdocumentor_windows_arm64.exe" (
        "%REPO_ROOT%\bin\gdocumentor_windows_arm64.exe" %*
        exit /b %ERRORLEVEL%
    )
) else (
    if exist "%REPO_ROOT%\bin\gdocumentor_windows_amd64.exe" (
        "%REPO_ROOT%\bin\gdocumentor_windows_amd64.exe" %*
        exit /b %ERRORLEVEL%
    )
)

where wsl.exe >nul 2>nul
if %ERRORLEVEL% EQU 0 (
    wsl.exe --exec gdocumentor %*
    exit /b %ERRORLEVEL%
)

echo {"success":false,"error":"Unable to locate gdocumentor.exe in PATH, %%USERPROFILE%%\.local\bin, or WSL2."} 1>&2
exit /b 1
