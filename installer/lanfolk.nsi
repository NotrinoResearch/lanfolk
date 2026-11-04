; Lanfolk installer (NSIS 3, Unicode, Modern UI 2).
;
; Compile:
;   makensis /DVERSION=1.0.0 /DBUILD_DIR=C:\path\to\build installer\lanfolk.nsi
;
; Defines:
;   VERSION    required, numeric x.y.z (shown in Apps and used for file version)
;   BUILD_DIR  required, absolute path of the folder that holds the input files
;   OUTFILE    optional, path of the installer to write
;              (default: BUILD_DIR\Lanfolk-VERSION-setup.exe)
;
; Files expected in BUILD_DIR:
;   lanfolk-service.exe  lanfolk-cli.exe  Lanfolk.exe  natprobe.exe  lanprobe.exe
;   wintun.dll  wintun-LICENSE.txt  LICENSE  THIRD_PARTY_NOTICES.md
;
; NSIS itself produces a 32-bit installer, which runs on 64-bit Windows and
; installs the 64-bit program into the 64-bit Program Files folder. The
; installer requires administrator rights and Windows 10 or later (64-bit).

Unicode true
SetCompressor /SOLID lzma
ManifestDPIAware true

!ifndef VERSION
  !error "Pass the version with /DVERSION=x.y.z"
!endif
!ifndef BUILD_DIR
  !error "Pass the input folder with /DBUILD_DIR=<absolute path>"
!endif
!ifndef OUTFILE
  !define OUTFILE "${BUILD_DIR}\Lanfolk-${VERSION}-setup.exe"
!endif

!define APP_NAME      "Lanfolk"
!define PUBLISHER     "Lanfolk contributors"
!define PROJECT_URL   "https://github.com/NotrinoResearch/lanfolk"
!define SERVICE_EXE   "lanfolk-service.exe"
!define UI_EXE        "Lanfolk.exe"
!define UNINST_KEY    "Software\Microsoft\Windows\CurrentVersion\Uninstall\Lanfolk"
!define RUN_KEY       "Software\Microsoft\Windows\CurrentVersion\Run"

Name "${APP_NAME}"
OutFile "${OUTFILE}"
InstallDir "$PROGRAMFILES64\Lanfolk"
RequestExecutionLevel admin
BrandingText "${APP_NAME} ${VERSION}"

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APP_NAME}"
VIAddVersionKey "CompanyName" "${PUBLISHER}"
VIAddVersionKey "LegalCopyright" "Apache License 2.0"
VIAddVersionKey "FileDescription" "${APP_NAME} installer"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"
!include "WinVer.nsh"
!include "FileFunc.nsh"

!insertmacro GetSize

; ---------------------------------------------------------------- pages ----

!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "Start ${APP_NAME}"
!define MUI_FINISHPAGE_RUN_FUNCTION LaunchUI
!define MUI_FINISHPAGE_LINK "Project page"
!define MUI_FINISHPAGE_LINK_LOCATION "${PROJECT_URL}"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "${BUILD_DIR}\LICENSE"
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; -------------------------------------------------------------- helpers ----

; CloseUI asks Lanfolk.exe to close, then ends it if it is still running.
; The UI is only a client of the service, so ending it loses no data.
!macro CloseUI
  ${DisableX64FSRedirection}
  nsExec::Exec '"$WINDIR\System32\taskkill.exe" /IM "${UI_EXE}"'
  Pop $0
  Sleep 1500
  nsExec::Exec '"$WINDIR\System32\taskkill.exe" /F /IM "${UI_EXE}"'
  Pop $0
  ${EnableX64FSRedirection}
!macroend

Function LaunchUI
  ; Start the UI as the normal user, not elevated: ask Explorer to open it.
  Exec '"$WINDIR\explorer.exe" "$INSTDIR\${UI_EXE}"'
FunctionEnd

Function .onInit
  ${IfNot} ${RunningX64}
    MessageBox MB_OK|MB_ICONSTOP "${APP_NAME} needs 64-bit Windows 10 or Windows 11." /SD IDOK
    Abort
  ${EndIf}
  ${IfNot} ${AtLeastWin10}
    MessageBox MB_OK|MB_ICONSTOP "${APP_NAME} needs Windows 10 or Windows 11." /SD IDOK
    Abort
  ${EndIf}
  SetRegView 64
  ; $INSTDIR from the registry is only valid for the 64-bit view.
  ReadRegStr $0 HKLM "${UNINST_KEY}" "InstallLocation"
  ${If} $0 != ""
    StrCpy $INSTDIR $0
  ${EndIf}
FunctionEnd

Function un.onInit
  SetRegView 64
FunctionEnd

; -------------------------------------------------------------- install ----

Section "${APP_NAME} (required)" SecMain
  SectionIn RO
  SetRegView 64
  SetShellVarContext all

  ; Upgrade: stop the old UI and remove the old service before the files
  ; (the service holds wintun.dll open). User data is not touched.
  !insertmacro CloseUI
  ${If} ${FileExists} "$INSTDIR\${SERVICE_EXE}"
    DetailPrint "Removing the previous service..."
    nsExec::ExecToLog '"$INSTDIR\${SERVICE_EXE}" uninstall'
    Pop $0
  ${EndIf}

  SetOutPath "$INSTDIR"
  SetOverwrite on
  File "${BUILD_DIR}\${SERVICE_EXE}"
  File "${BUILD_DIR}\lanfolk-cli.exe"
  File "${BUILD_DIR}\${UI_EXE}"
  File "${BUILD_DIR}\natprobe.exe"
  File "${BUILD_DIR}\lanprobe.exe"
  File "${BUILD_DIR}\wintun.dll"
  File "${BUILD_DIR}\wintun-LICENSE.txt"
  File "${BUILD_DIR}\LICENSE"
  File "${BUILD_DIR}\THIRD_PARTY_NOTICES.md"

  ; Uninstaller and Apps entry first, so a failed service setup can still
  ; be cleaned up from Apps.
  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr   HKLM "${UNINST_KEY}" "DisplayName" "${APP_NAME}"
  WriteRegStr   HKLM "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr   HKLM "${UNINST_KEY}" "Publisher" "${PUBLISHER}"
  WriteRegStr   HKLM "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr   HKLM "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${UI_EXE}"
  WriteRegStr   HKLM "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr   HKLM "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegStr   HKLM "${UNINST_KEY}" "URLInfoAbout" "${PROJECT_URL}"
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  WriteRegDWORD HKLM "${UNINST_KEY}" "EstimatedSize" $0

  ; Start Menu shortcuts (all users).
  CreateDirectory "$SMPROGRAMS\${APP_NAME}"
  CreateShortCut "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk" "$INSTDIR\${UI_EXE}" "" "$INSTDIR\${UI_EXE}" 0
  CreateShortCut "$SMPROGRAMS\${APP_NAME}\Uninstall ${APP_NAME}.lnk" "$INSTDIR\uninstall.exe"

  ; Register the auto-start service (also creates the firewall rules and
  ; records the installing user as the owner of the control pipe), then
  ; start it. Errors are shown.
  DetailPrint "Registering the ${APP_NAME} service..."
  nsExec::ExecToStack '"$INSTDIR\${SERVICE_EXE}" install'
  Pop $0
  Pop $1
  ${If} $0 != "0"
    DetailPrint "Service install failed: exit code $0"
    MessageBox MB_OK|MB_ICONSTOP "The ${APP_NAME} service could not be installed (exit code $0).$\r$\n$\r$\n$1$\r$\n$\r$\nSetup will stop. To retry, open an elevated command prompt and run:$\r$\n$\"$INSTDIR\${SERVICE_EXE}$\" install$\r$\nTo remove what was installed, use Apps in Windows Settings." /SD IDOK
    SetErrorLevel 3
    Abort
  ${EndIf}

  DetailPrint "Starting the ${APP_NAME} service..."
  nsExec::ExecToStack '"$INSTDIR\${SERVICE_EXE}" start'
  Pop $0
  Pop $1
  ${If} $0 != "0"
    DetailPrint "Service start failed: exit code $0"
    MessageBox MB_OK|MB_ICONEXCLAMATION "The ${APP_NAME} service was installed but could not be started (exit code $0).$\r$\n$\r$\n$1$\r$\n$\r$\nIt will be started again at the next restart, or you can start it from an elevated command prompt:$\r$\n$\"$INSTDIR\${SERVICE_EXE}$\" start" /SD IDOK
  ${EndIf}
SectionEnd

Section /o "Start ${APP_NAME} when I sign in" SecAutostart
  ; Per-user setting of the account that runs the installer.
  WriteRegStr HKCU "${RUN_KEY}" "${APP_NAME}" '"$INSTDIR\${UI_EXE}"'
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecMain} "The ${APP_NAME} service, the desktop app, the command line tool and the Wintun adapter driver library."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecAutostart} "Open the ${APP_NAME} window automatically when you sign in to Windows. The service itself always starts with Windows."
!insertmacro MUI_FUNCTION_DESCRIPTION_END

; ------------------------------------------------------------ uninstall ----

Section "Uninstall"
  SetRegView 64
  SetShellVarContext all

  ; 1. Close the app.
  !insertmacro CloseUI

  ; 2. Stop and remove the service, firewall rules and adapter. The data
  ;    folder is not touched by this step.
  ${If} ${FileExists} "$INSTDIR\${SERVICE_EXE}"
    DetailPrint "Removing the ${APP_NAME} service..."
    nsExec::ExecToStack '"$INSTDIR\${SERVICE_EXE}" uninstall'
    Pop $0
    Pop $1
    ${If} $0 != "0"
      DetailPrint "Service uninstall failed: exit code $0"
      MessageBox MB_OK|MB_ICONEXCLAMATION "The ${APP_NAME} service could not be removed completely (exit code $0).$\r$\n$\r$\n$1$\r$\n$\r$\nThe files will be removed anyway. If the service remains, remove it from an elevated command prompt with:$\r$\nsc delete Lanfolk" /SD IDOK
    ${EndIf}
  ${EndIf}

  ; 3. Remove files, shortcuts and registry entries.
  Delete "$INSTDIR\${SERVICE_EXE}"
  Delete "$INSTDIR\lanfolk-cli.exe"
  Delete "$INSTDIR\${UI_EXE}"
  Delete "$INSTDIR\natprobe.exe"
  Delete "$INSTDIR\lanprobe.exe"
  Delete "$INSTDIR\wintun.dll"
  Delete "$INSTDIR\wintun-LICENSE.txt"
  Delete "$INSTDIR\LICENSE"
  Delete "$INSTDIR\THIRD_PARTY_NOTICES.md"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  Delete "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk"
  Delete "$SMPROGRAMS\${APP_NAME}\Uninstall ${APP_NAME}.lnk"
  RMDir "$SMPROGRAMS\${APP_NAME}"

  DeleteRegValue HKCU "${RUN_KEY}" "${APP_NAME}"
  DeleteRegKey HKLM "${UNINST_KEY}"

  ; 4. Ask about the user data. The default answer is No, also in silent mode.
  ReadEnvStr $R0 "ProgramData"
  ${If} $R0 == ""
    StrCpy $R0 "$APPDATA"
  ${EndIf}
  StrCpy $R0 "$R0\Lanfolk"
  ${If} ${FileExists} "$R0\*.*"
    MessageBox MB_YESNO|MB_ICONQUESTION|MB_DEFBUTTON2 "Do you also want to delete your ${APP_NAME} data?$\r$\n$\r$\n$R0$\r$\n$\r$\nThis contains your identity (friend code), friends, networks, settings and logs. If you delete it, your friends must add you again after a reinstall.$\r$\n$\r$\nChoose No to keep it." /SD IDNO IDYES deleteData IDNO keepData
    deleteData:
      RMDir /r "$R0"
      DetailPrint "Deleted $R0"
      Goto dataDone
    keepData:
      DetailPrint "Kept $R0"
    dataDone:
  ${EndIf}
SectionEnd
