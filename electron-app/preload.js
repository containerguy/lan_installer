const { contextBridge, ipcRenderer } = require('electron');
const fs = require('fs');
const path = require('path');
const { dialog } = require('electron');

contextBridge.exposeInMainWorld('api', {
  install: (config) => {
    // schreibe config in JSON-Datei
    const configPath = path.resolve(__dirname, '../config_gui.json');
    fs.writeFileSync(configPath, JSON.stringify(config, null, 2));
    ipcRenderer.send('install', configPath);
  },
  receive: (channel, func) => {
    ipcRenderer.on(channel, (event, ...args) => func(...args));
  },
  selectFolder: async () => {
    return await ipcRenderer.invoke('select-folder');
  },
  getGames: () => {
    const gamesDir = path.resolve(__dirname, '../spiele');
    if (!fs.existsSync(gamesDir)) return [];
    return fs.readdirSync(gamesDir).filter(f =>
      fs.statSync(path.join(gamesDir, f)).isDirectory()
    );
  },
  getISOs: () => {
    const isoDir = path.resolve(__dirname, '../isos');
    if (!fs.existsSync(isoDir)) return [];
    return fs.readdirSync(isoDir).filter(f => f.endsWith('.iso'));
  }
});
