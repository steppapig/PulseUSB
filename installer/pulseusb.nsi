; Windows setup program for PulseUSB. Build from the repository root, after
; building dist/PulseUSB.exe:
;
;   makensis -DVERSION=0.24.2 installer/pulseusb.nsi
;
; It installs for the current user only (no administrator rights needed).
; The chosen folder is recorded in the registry, where the app looks it up.

Unicode true
!include "MUI2.nsh"
!include "LogicLib.nsh"

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!define APP "PulseUSB"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP}"

Name "${APP}"
OutFile "../dist/PulseUSB-Setup.exe"
InstallDir "$LOCALAPPDATA\Programs\${APP}"
; An update goes where the app was installed before.
InstallDirRegKey HKCU "${UNINSTKEY}" "InstallLocation"
RequestExecutionLevel user
SetCompressor /SOLID lzma

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APP}"
VIAddVersionKey "FileDescription" "${APP} setup"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "CompanyName" "PulseUSB open-source project"
VIAddVersionKey "LegalCopyright" "Open source; see the project repository"

!define MUI_ICON "../cmd/pulseusb-gui/icon.ico"
!define MUI_UNICON "../cmd/pulseusb-gui/icon.ico"
!define MUI_WELCOMEPAGE_TEXT "This installs ${APP} ${VERSION} for your Windows account.$\r$\n$\r$\n${APP} lets this PC use USB devices that are plugged into another computer running the PulseUSB server.$\r$\n$\r$\nIt also installs the free usbip-win2 USB driver if this PC does not have it yet. Windows will ask for administrator permission for that.$\r$\n$\r$\nIf ${APP} is running it will be closed and restarted. Devices that are connected stay connected."
!define MUI_FINISHPAGE_RUN "$INSTDIR\${APP}.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Start ${APP}"

!define MUI_DIRECTORYPAGE_TEXT_TOP "Setup will install ${APP} in the following folder. To install somewhere else, click Browse and choose another folder.$\r$\n$\r$\nThis installs for your account without administrator rights, so pick a folder you are allowed to write to (not Program Files)."
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "Install"
  ; A running copy locks its own file: stop it first. (usbeth-gui.exe is the
  ; app's name from before it was called PulseUSB.)
  nsExec::Exec 'taskkill /F /IM ${APP}.exe'
  nsExec::Exec 'taskkill /F /IM usbeth-gui.exe'
  Sleep 700

  ; Moving to another folder: take the copy in the old one away.
  ReadRegStr $0 HKCU "${UNINSTKEY}" "InstallLocation"
  ${If} $0 != ""
  ${AndIf} $0 != "$INSTDIR"
    Delete "$0\${APP}.exe"
    Delete "$0\background.txt"
    Delete "$0\Uninstall.exe"
    RMDir "$0"
  ${EndIf}

  ClearErrors
  SetOutPath "$INSTDIR"
  File "../dist/PulseUSB.exe"
  ${If} ${Errors}
    MessageBox MB_OK|MB_ICONSTOP "Setup could not write to $INSTDIR.$\r$\n$\r$\nChoose a folder you are allowed to write to without administrator rights."
    Abort
  ${EndIf}
  File "/oname=Licenses.txt" "../internal/licenses/THIRD_PARTY_LICENSES.txt"

  ; If the usbip-win2 installer was placed in installer/vendor when this
  ; setup program was built, it is carried along, so the driver can be
  ; installed without an internet connection.
!if /FileExists "vendor/usbip-win2-setup.exe"
  SetOutPath "$INSTDIR\driver"
  File "vendor/usbip-win2-setup.exe"
  File "/oname=LICENSE.txt" "vendor/usbip-win2-LICENSE.txt"
  SetOutPath "$INSTDIR"
!endif

  ; The USB driver PulseUSB relies on (usbip-win2). The app runs that
  ; project's installer silently (the bundled copy, or else a download);
  ; Windows asks for administrator permission once. Nothing happens if the
  ; driver is already there.
  DetailPrint "Installing the USB driver (usbip-win2) if needed; Windows may ask for permission..."
  nsExec::Exec '"$INSTDIR\${APP}.exe" -install-driver'
  Pop $0
  ${If} $0 != "0"
    DetailPrint "The USB driver could not be installed now. PulseUSB will offer to install it when it starts."
  ${Else}
    DetailPrint "USB driver ready."
  ${EndIf}

  WriteUninstaller "$INSTDIR\Uninstall.exe"
  CreateShortcut "$SMPROGRAMS\${APP}.lnk" "$INSTDIR\${APP}.exe"

  ; Listing under "Installed apps".
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayName" "${APP}"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTKEY}" "Publisher" "PulseUSB open-source project"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\${APP}.exe"
  WriteRegStr HKCU "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTKEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegStr HKCU "${UNINSTKEY}" "QuietUninstallString" '"$INSTDIR\Uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoRepair" 1
  WriteRegDWORD HKCU "${UNINSTKEY}" "EstimatedSize" 12500
SectionEnd

Section "Uninstall"
  nsExec::Exec 'taskkill /F /IM ${APP}.exe'
  Sleep 700

  ; Launch at Windows startup, and the optional start before sign-in. The
  ; latter was created with administrator rights; removing it here only
  ; succeeds if this uninstaller has them, and is harmless otherwise (the
  ; task then points at a program that no longer exists).
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "${APP}"
  nsExec::Exec 'schtasks /Delete /TN ${APP}-background /F'

  Delete "$SMPROGRAMS\${APP}.lnk"
  Delete "$INSTDIR\${APP}.exe"
  Delete "$INSTDIR\background.txt"
  Delete "$INSTDIR\Licenses.txt"
  Delete "$INSTDIR\driver\usbip-win2-setup.exe"
  Delete "$INSTDIR\driver\LICENSE.txt"
  RMDir "$INSTDIR\driver"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"
  RMDir /r "$LOCALAPPDATA\${APP}"   ; the window's cache

  MessageBox MB_YESNO|MB_ICONQUESTION|MB_DEFBUTTON2 "Also remove your ${APP} settings (servers, device names, groups)?" /SD IDNO IDNO keep
    RMDir /r "$APPDATA\${APP}"
  keep:

  DeleteRegKey HKCU "${UNINSTKEY}"
SectionEnd
