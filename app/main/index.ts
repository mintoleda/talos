import {
  app,
  BrowserWindow,
  Menu,
  dialog,
  ipcMain,
  shell,
  type MenuItemConstructorOptions,
} from 'electron'
import { join } from 'node:path'
import { ensureDaemon, restartDaemon } from './daemon'

const gotLock = app.requestSingleInstanceLock()
if (!gotLock) {
  app.quit()
} else {
  app.on('second-instance', () => {
    const win = BrowserWindow.getAllWindows()[0]
    if (win) {
      if (win.isMinimized()) win.restore()
      win.focus()
    }
  })
}

let mainWindow: BrowserWindow | null = null

function preloadPath(): string {
  // electron-vite places preload next to main under out/preload/
  return join(__dirname, '../preload/preload.js')
}

function createWindow(): void {
  mainWindow = new BrowserWindow({
    width: 1100,
    height: 800,
    show: false,
    webPreferences: {
      preload: preloadPath(),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  })

  mainWindow.on('ready-to-show', () => {
    mainWindow?.show()
  })

  mainWindow.webContents.setWindowOpenHandler((details) => {
    void shell.openExternal(details.url)
    return { action: 'deny' }
  })

  if (process.env.ELECTRON_RENDERER_URL) {
    void mainWindow.loadURL(process.env.ELECTRON_RENDERER_URL)
  } else {
    void mainWindow.loadFile(join(__dirname, '../renderer/index.html'))
  }

  mainWindow.on('closed', () => {
    mainWindow = null
  })
}

function buildMenu(): void {
  const isMac = process.platform === 'darwin'
  const template: MenuItemConstructorOptions[] = [
    ...(isMac
      ? [
          {
            label: app.name,
            submenu: [
              { role: 'about' as const },
              { type: 'separator' as const },
              { role: 'services' as const },
              { type: 'separator' as const },
              { role: 'hide' as const },
              { role: 'hideOthers' as const },
              { role: 'unhide' as const },
              { type: 'separator' as const },
              { role: 'quit' as const },
            ],
          },
        ]
      : []),
    {
      label: 'File',
      submenu: [
        {
          label: 'New Session',
          accelerator: 'CommandOrControl+N',
          click: () => {
            BrowserWindow.getFocusedWindow()?.webContents.send('menu:new-session')
          },
        },
        { type: 'separator' },
        isMac ? { role: 'close' } : { role: 'quit' },
      ],
    },
    {
      label: 'View',
      submenu: [
        { role: 'reload' },
        { role: 'forceReload' },
        { role: 'toggleDevTools' },
        { type: 'separator' },
        { role: 'resetZoom' },
        { role: 'zoomIn' },
        { role: 'zoomOut' },
        { type: 'separator' },
        { role: 'togglefullscreen' },
      ],
    },
    {
      label: 'Window',
      submenu: [{ role: 'minimize' }, { role: 'zoom' }, ...(isMac ? [{ role: 'front' as const }] : [])],
    },
  ]
  Menu.setApplicationMenu(Menu.buildFromTemplate(template))
}

function registerIPC(): void {
  ipcMain.handle('daemon:ensure', async () => ensureDaemon())
  ipcMain.handle('daemon:restart', async () => restartDaemon())
  ipcMain.handle('dialog:pickDirectory', async () => {
    const win = BrowserWindow.getFocusedWindow() ?? mainWindow
    const opts: Electron.OpenDialogOptions = {
      properties: ['openDirectory', 'createDirectory'],
    }
    const result = win
      ? await dialog.showOpenDialog(win, opts)
      : await dialog.showOpenDialog(opts)
    if (result.canceled || result.filePaths.length === 0) return null
    return result.filePaths[0]
  })
  ipcMain.handle('shell:openExternal', async (_e, url: string) => {
    await shell.openExternal(url)
  })
  ipcMain.handle('shell:showItemInFolder', async (_e, path: string) => {
    shell.showItemInFolder(path)
  })
  ipcMain.handle('app:getVersion', () => app.getVersion())
  ipcMain.handle('app:setBadgeCount', (_e, n: number) => {
    app.setBadgeCount(typeof n === 'number' && n > 0 ? Math.floor(n) : 0)
  })
}

app.whenReady().then(() => {
  registerIPC()
  buildMenu()
  createWindow()

  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) {
      createWindow()
    }
  })
})

// Close windows only — daemon keeps running.
app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') {
    app.quit()
  }
})
