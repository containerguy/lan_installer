const { app, BrowserWindow, ipcMain, dialog } = require("electron");
const path = require("path");
const { spawn } = require("child_process");
const fs = require("fs");

let mainWindow;

function createWindow () {
  mainWindow = new BrowserWindow({
    width: 1000,
    height: 700,
    webPreferences: {
      preload: path.join(__dirname, "preload.js"),
      contextIsolation: true,
      nodeIntegration: false
    }
  });
  mainWindow.setMenuBarVisibility(false);
  mainWindow.loadFile("index.html");
}

app.whenReady().then(createWindow);

ipcMain.on("install", (event, configPath) => {
  const psCmd = fs.existsSync("C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe")
    ? "powershell.exe"
    : "pwsh.exe";

  const ps = spawn(psCmd, [
    "-ExecutionPolicy", "Bypass",
    "-File", "install.ps1",
    "-Config", configPath
  ]);

  ps.stdout.on("data", data => {
    mainWindow.webContents.send("log", data.toString());
  });

  ps.stderr.on("data", data => {
    mainWindow.webContents.send("log", "Fehler: " + data.toString());
  });

  ps.on("close", code => {
    mainWindow.webContents.send("log", `Prozess beendet mit Code ${code}`);
    mainWindow.webContents.send("progress", 100);
  });
});

ipcMain.handle("select-folder", async () => {
  const result = await dialog.showOpenDialog({ properties: ["openDirectory"] });
  return result.canceled ? null : result.filePaths[0];
});
