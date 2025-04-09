const { contextBridge, ipcRenderer } = require('electron');
const fs = require('fs');
const path = require('path');
const { dialog } = require('electron');

const baseDir = path.resolve(__dirname, '..');
const pathsToCheck = [
  'spiele',
  'isos',
  'config_gui.json'
];

contextBridge.exposeInMainWorld('api', {
  install: (config) => {
    const configPath = path.join(baseDir, 'config_gui.json');
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
    const dir = path.join(baseDir, 'spiele');
    if (!fs.existsSync(dir)) return [];
    return fs.readdirSync(dir).filter(f =>
      fs.statSync(path.join(dir, f)).isDirectory()
    );
  },
  getISOs: () => {
    const dir = path.join(baseDir, 'isos');
    if (!fs.existsSync(dir)) return [];
    return fs.readdirSync(dir).filter(f => f.endsWith('.iso'));
  },
  checkStructure: () => {
    const missing = [];
    for (const p of pathsToCheck) {
      const full = path.join(baseDir, p);
      if (!fs.existsSync(full)) missing.push(p);
    }
    return missing;
  },
  createStructure: () => {
    for (const p of pathsToCheck) {
      const full = path.join(baseDir, p);
      if (!fs.existsSync(full)) {
        if (p.endsWith('.json')) fs.writeFileSync(full, '{}');
        else fs.mkdirSync(full, { recursive: true });
      }
    }
    return true;
  }
});


const { shell } = require('electron');

contextBridge.exposeInMainWorld('api', {
  ...module.exports,
  exportLog: (logContent) => {
    const logPath = path.join(baseDir, 'install-log.txt');
    fs.writeFileSync(logPath, logContent);
    shell.showItemInFolder(logPath);
  },
  importDropped: (isoPaths, folderPaths) => {
    const isoDir = path.join(baseDir, 'isos');
    const gamesDir = path.join(baseDir, 'spiele');
    if (!fs.existsSync(isoDir)) fs.mkdirSync(isoDir);
    if (!fs.existsSync(gamesDir)) fs.mkdirSync(gamesDir);
    isoPaths.forEach(src => {
      const dest = path.join(isoDir, path.basename(src));
      fs.copyFileSync(src, dest);
    });
    folderPaths.forEach(src => {
      const dest = path.join(gamesDir, path.basename(src));
      fs.cpSync(src, dest, { recursive: true });
    });
  }
});
