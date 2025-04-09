const { app, BrowserWindow, ipcMain } = require('electron')
const path = require('path')
const { spawn } = require('child_process')

let mainWindow

function createWindow () {
  mainWindow = new BrowserWindow({
    width: 1000,
    height: 700,
    webPreferences: {
      preload: path.join(__dirname, 'preload.js')
    }
  })
  mainWindow.loadFile('index.html')
}

app.whenReady().then(createWindow)

ipcMain.on('install', (config) => {
  const ps = spawn('powershell.exe', ['-File', 'install.ps1'])

  ps.stdout.on('data', (data) => {
    mainWindow.webContents.send('log', data.toString())
    // Optional: Fortschritt interpretieren
    if (data.toString().match(/Installiere|Kopiere|Mounting/)) {
      mainWindow.webContents.send('progress', 10)
    }
  })

  ps.stderr.on('data', (data) => {
    mainWindow.webContents.send('log', 'Fehler: ' + data.toString())
  })

  ps.on('close', (code) => {
    mainWindow.webContents.send('log', `Installationsprozess beendet mit Code ${code}`)
    mainWindow.webContents.send('progress', 100)
  })
})
