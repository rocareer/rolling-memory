@echo off
REM Rolling-Memory Windows MCP launcher for plugin hosts.
setlocal
if defined ROLLING_MEMORY_BIN (
  "%ROLLING_MEMORY_BIN%" mcp %*
  exit /b %ERRORLEVEL%
)
if exist "%LOCALAPPDATA%\rolling-memory\bin\rolling-memory.cmd" (
  call "%LOCALAPPDATA%\rolling-memory\bin\rolling-memory.cmd" mcp %*
  exit /b %ERRORLEVEL%
)
if exist "%LOCALAPPDATA%\rolling-memory\bin\rolling-memory.exe" (
  "%LOCALAPPDATA%\rolling-memory\bin\rolling-memory.exe" mcp %*
  exit /b %ERRORLEVEL%
)
rolling-memory.exe mcp %*
