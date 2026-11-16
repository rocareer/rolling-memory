@echo off
REM Rolling-Memory Windows Hook launcher for plugin hosts.
setlocal
set SUB=%1
shift
if "%SUB%"=="" exit /b 0
if defined ROLLING_MEMORY_BIN (
  "%ROLLING_MEMORY_BIN%" hook %SUB% %1 %2 %3 %4
  exit /b 0
)
if exist "%LOCALAPPDATA%\rolling-memory\bin\rolling-memory.cmd" (
  call "%LOCALAPPDATA%\rolling-memory\bin\rolling-memory.cmd" hook %SUB% %1 %2 %3 %4
  exit /b 0
)
if exist "%LOCALAPPDATA%\rolling-memory\bin\rolling-memory.exe" (
  "%LOCALAPPDATA%\rolling-memory\bin\rolling-memory.exe" hook %SUB% %1 %2 %3 %4
  exit /b 0
)
exit /b 0
