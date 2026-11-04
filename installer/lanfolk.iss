; Lanfolk installer (Inno Setup 6.7 or newer, Unicode).
;
; Compile:
;   ISCC.exe /DVERSION=1.0.0 /DBUILD_DIR=C:\path\to\build /OC:\path\to\dist installer\lanfolk.iss
;
; Defines:
;   VERSION    required, numeric x.y.z (shown in Apps and used for file version)
;   BUILD_DIR  required, absolute path of the folder that holds the input files
;   /O<dir>    optional, output folder (default: BUILD_DIR)
;
; Output: <output folder>\Lanfolk-<VERSION>-setup.exe
;
; Files expected in BUILD_DIR:
;   lanfolk-service.exe  lanfolk-cli.exe  Lanfolk.exe  natprobe.exe  lanprobe.exe
;   wintun.dll  wintun-LICENSE.txt  LICENSE  THIRD_PARTY_NOTICES.md
;
; Also needed (relative to this file):
;   ..\ui\assets\lanfolk.ico
;   assets\wizard-large.png  assets\wizard-large@2x.png
;   assets\wizard-small.png  assets\wizard-small@2x.png
;
; The installer is a native 64-bit program. It requires administrator rights
; and Windows 10 or later (64-bit). Behavior is the same as the former NSIS
; script (see docs/DECISIONS.md D-026).
;
; Everything the user reads lives in [Messages] and [CustomMessages], so the
; texts can be translated later without touching the logic.

#ifndef VERSION
  #error "Pass the version with /DVERSION=x.y.z"
#endif
#ifndef BUILD_DIR
  #error "Pass the input folder with /DBUILD_DIR=<absolute path>"
#endif

#define AppName      "Lanfolk"
#define Publisher    "Lanfolk contributors"
#define ProjectUrl   "https://github.com/NotrinoResearch/lanfolk"
#define ServiceExe   "lanfolk-service.exe"
#define UiExe        "Lanfolk.exe"
#define AppUserModel "NotrinoResearch.Lanfolk"

[Setup]
; The AppId must never change: upgrades and Apps & Features rely on it.
AppId={{858B2C4A-8079-43ED-B101-A6DB9595B87D}
AppName={#AppName}
AppVersion={#VERSION}
AppVerName={#AppName} {#VERSION}
AppPublisher={#Publisher}
AppPublisherURL={#ProjectUrl}
AppSupportURL={#ProjectUrl}/issues
AppUpdatesURL={#ProjectUrl}/releases
AppCopyright=Apache License 2.0
VersionInfoVersion={#VERSION}.0
VersionInfoCompany={#Publisher}
VersionInfoDescription={#AppName} installer
VersionInfoCopyright=Apache License 2.0
VersionInfoProductName={#AppName}
VersionInfoProductVersion={#VERSION}

; 64-bit only, Windows 10 or later, administrator.
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
PrivilegesRequired=admin
; The auto-start value is written to HKCU of the installing account on purpose.
UsedUserAreasWarning=no
DefaultDirName={code:GetDefaultDir}
DisableDirPage=auto
DisableProgramGroupPage=yes
DefaultGroupName={#AppName}
SetupMutex=LanfolkSetupMutex

; The installer closes the app and replaces the service itself (see [Code]).
CloseApplications=no
RestartApplications=no
ChangesEnvironment=yes

; Wizard: modern, follows the Windows light or dark setting, few clicks.
WizardStyle=modern dynamic
; The wizard window is not resizable (WizardResizable is obsolete since 6.6).
WizardSizePercent=110
DisableWelcomePage=no
DisableReadyPage=yes
LicenseFile={#BUILD_DIR}\LICENSE
SetupIconFile=..\ui\assets\lanfolk.ico
WizardImageFile=assets\wizard-large.png,assets\wizard-large@2x.png
WizardImageFileDynamicDark=assets\wizard-large.png,assets\wizard-large@2x.png
WizardSmallImageFile=assets\wizard-small.png,assets\wizard-small@2x.png
WizardSmallImageFileDynamicDark=assets\wizard-small.png,assets\wizard-small@2x.png
; Ink, so the art panel blends with its edges in light and dark mode.
WizardImageBackColor=#2F5BEA
WizardImageBackColorDynamicDark=#2F5BEA

; Apps & Features
UninstallDisplayName={#AppName}
UninstallDisplayIcon={app}\{#UiExe}

; Output
OutputDir={#BUILD_DIR}
OutputBaseFilename=Lanfolk-{#VERSION}-setup
Compression=lzma2/ultra64
SolidCompression=yes

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Messages]
SetupAppTitle=Set up {#AppName}
SetupWindowTitle=Set up %1
WelcomeLabel1=Welcome to {#AppName}
WelcomeLabel2=Lanfolk puts you and your friends on one private local network over the internet, so LAN games just work. Setup installs the Lanfolk service, the app and the Wintun network driver.%n%nClick Next to continue.
SelectTasksLabel2=Choose how Lanfolk fits into Windows. You can run Setup again later to change these.
FinishedHeadingLabel=You are all set
FinishedLabel=Lanfolk is ready. Create a network and send the invite to your friends, or paste an invite you received.
FinishedLabelNoIcons=Lanfolk is ready. Create a network and send the invite to your friends, or paste an invite you received.
ConfirmUninstall=Do you want to remove %1 from this computer?

[CustomMessages]
english.TaskAutostart=Start Lanfolk when I sign in
english.TaskDesktopIcon=Create a desktop shortcut
english.TaskAddToPath=Add the command-line tools to PATH
english.LaunchLanfolk=Launch Lanfolk
english.StatusClosingApp=Closing Lanfolk...
english.StatusRemovingService=Removing the previous Lanfolk service...
english.StatusInstallingService=Registering the Lanfolk service...
english.StatusStartingService=Starting the Lanfolk service...
english.ServiceInstallFailed=The Lanfolk service could not be installed (exit code %1).%n%n%2%n%nSetup could not finish. To retry, open an elevated command prompt and run:%n"%3" install%nTo remove what was installed, use Apps in Windows Settings.
english.ServiceStartFailed=The Lanfolk service was installed but could not be started (exit code %1).%n%n%2%n%nIt will be started again at the next restart, or you can start it from an elevated command prompt:%n"%3" start
english.ServiceUninstallFailed=The Lanfolk service could not be removed completely (exit code %1).%n%n%2%n%nThe files will be removed anyway. If the service remains, remove it from an elevated command prompt with:%nsc delete Lanfolk
english.FinishedServiceFailedHeading=Setup could not finish
english.FinishedServiceFailed=Lanfolk was copied to this computer, but its service could not be installed, so the app cannot connect yet.%n%nSetup has shown the reason and the command to retry. To remove what was installed, use Apps in Windows Settings.
english.DeleteDataPrompt=Do you also want to delete your Lanfolk data?%n%n%1%n%nThis contains your identity (friend code), friends, networks, settings and logs. If you delete it, your friends must add you again after a reinstall.%n%nChoose No to keep it.
english.DataDeleted=Deleted %1
english.DataKept=Kept %1

[Tasks]
Name: "autostart"; Description: "{cm:TaskAutostart}"
Name: "desktopicon"; Description: "{cm:TaskDesktopIcon}"; Flags: unchecked
Name: "addtopath"; Description: "{cm:TaskAddToPath}"

[Files]
Source: "{#BUILD_DIR}\{#ServiceExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#BUILD_DIR}\lanfolk-cli.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#BUILD_DIR}\{#UiExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#BUILD_DIR}\natprobe.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#BUILD_DIR}\lanprobe.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#BUILD_DIR}\wintun.dll"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#BUILD_DIR}\wintun-LICENSE.txt"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#BUILD_DIR}\LICENSE"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#BUILD_DIR}\THIRD_PARTY_NOTICES.md"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
; Directly in the Start menu (no folder, no uninstall shortcut), like Windows 11 apps.
Name: "{autoprograms}\{#AppName}"; Filename: "{app}\{#UiExe}"; AppUserModelID: "{#AppUserModel}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#UiExe}"; AppUserModelID: "{#AppUserModel}"; Tasks: desktopicon

[Registry]
; Per-user setting of the account that runs the installer. Removed by the
; uninstaller; also removed on upgrade when the task is not selected (see [Code]).
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "{#AppName}"; ValueData: """{app}\{#UiExe}"" --minimized"; Flags: uninsdeletevalue; Tasks: autostart

[Run]
; Started as the original (non-elevated) user.
Filename: "{app}\{#UiExe}"; Description: "{cm:LaunchLanfolk}"; Flags: nowait postinstall skipifsilent runasoriginaluser; Check: ServiceReady

[Code]
const
  ServiceExeName = '{#ServiceExe}';
  UiExeName = '{#UiExe}';
  AppDisplayName = '{#AppName}';
  // Uninstall key of the former NSIS installer.
  NsisUninstallKey = 'Software\Microsoft\Windows\CurrentVersion\Uninstall\Lanfolk';
  RunKey = 'Software\Microsoft\Windows\CurrentVersion\Run';
  EnvironmentKey = 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment';

var
  ServiceFailed: Boolean;

// ------------------------------------------------------------- helpers ----

function IsSilentRun: Boolean;
begin
  if IsUninstaller then
    Result := UninstallSilent
  else
    Result := WizardSilent;
end;

// Shows a message unless Setup or Uninstall runs silently; it is always logged.
procedure Notify(const Text: String; const Kind: TMsgBoxType);
begin
  Log(Text);
  if not IsSilentRun then
    SuppressibleMsgBox(Text, Kind, MB_OK, IDOK);
end;

function FormatMsg(const Name: String; const Args: array of String): String;
begin
  Result := FmtMessage(CustomMessage(Name), Args);
end;

procedure SetStatus(const MessageName: String);
begin
  if (not IsUninstaller) and (not WizardSilent) then
  begin
    WizardForm.StatusLabel.Caption := CustomMessage(MessageName);
    WizardForm.StatusLabel.Update;
  end;
end;

function TrimTrailingBackslash(const S: String): String;
begin
  Result := S;
  while (Length(Result) > 3) and (Result[Length(Result)] = '\') do
    Delete(Result, Length(Result), 1);
end;

// Runs a program hidden and returns its exit code and combined output
// (-1 when it could not be started).
function RunCaptured(const Exe, Args: String; var Output: String): Integer;
var
  OutFile, Params: String;
  ResultCode: Integer;
  Raw: AnsiString;
begin
  Output := '';
  OutFile := ExpandConstant('{tmp}\lanfolk-service-output.txt');
  DeleteFile(OutFile);
  // cmd /S /C strips the outer quotes, so every path may contain spaces.
  Params := '/S /C ""' + Exe + '" ' + Args + ' > "' + OutFile + '" 2>&1"';
  if not Exec(ExpandConstant('{cmd}'), Params, '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
  begin
    Output := SysErrorMessage(ResultCode);
    Result := -1;
    Exit;
  end;
  if LoadStringFromFile(OutFile, Raw) then
    Output := Trim(String(Raw));
  if Length(Output) > 1500 then
    Output := Copy(Output, 1, 1500) + '...';
  DeleteFile(OutFile);
  Result := ResultCode;
end;

// Asks Lanfolk.exe to close, then ends it if it is still running. The UI is
// only a client of the service, so ending it loses no data.
procedure CloseUI;
var
  TaskKill: String;
  ResultCode: Integer;
begin
  TaskKill := ExpandConstant('{sys}\taskkill.exe');
  if Exec(TaskKill, '/IM "' + UiExeName + '"', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
  begin
    // Exit code 0 means a window was asked to close: give it a moment.
    if ResultCode = 0 then
      Sleep(1500);
    Exec(TaskKill, '/F /IM "' + UiExeName + '"', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  end;
end;

// Stops and removes a service registered by the lanfolk-service.exe in Dir.
// The data folder is not touched. Returns True when there was nothing to do
// or the removal succeeded.
function RemoveService(const Dir: String; var ExitCode: Integer; var Output: String): Boolean;
var
  Exe: String;
begin
  Exe := AddBackslash(Dir) + ServiceExeName;
  ExitCode := 0;
  Output := '';
  Result := True;
  if FileExists(Exe) then
  begin
    ExitCode := RunCaptured(Exe, 'uninstall', Output);
    Result := ExitCode = 0;
  end;
end;

// ---------------------------------------------------------------- PATH ----

function SamePath(const A, B: String): Boolean;
begin
  Result := CompareText(TrimTrailingBackslash(Trim(A)), TrimTrailingBackslash(Trim(B))) = 0;
end;

function PathHas(const Paths, Dir: String): Boolean;
var
  Rest, Item: String;
  P: Integer;
begin
  Result := False;
  Rest := Paths;
  while Rest <> '' do
  begin
    P := Pos(';', Rest);
    if P = 0 then
    begin
      Item := Rest;
      Rest := '';
    end else
    begin
      Item := Copy(Rest, 1, P - 1);
      Rest := Copy(Rest, P + 1, Length(Rest));
    end;
    if (Item <> '') and SamePath(Item, Dir) then
    begin
      Result := True;
      Exit;
    end;
  end;
end;

procedure AddToPath(const Dir: String);
var
  Paths: String;
begin
  if not RegQueryStringValue(HKLM64, EnvironmentKey, 'Path', Paths) then
    Paths := '';
  if PathHas(Paths, Dir) then
    Exit;
  if (Paths <> '') and (Paths[Length(Paths)] <> ';') then
    Paths := Paths + ';';
  if RegWriteExpandStringValue(HKLM64, EnvironmentKey, 'Path', Paths + Dir) then
    Log('Added to PATH: ' + Dir)
  else
    Log('Could not write the system PATH.');
end;

procedure RemoveFromPath(const Dir: String);
var
  Paths, Rest, Item, NewPaths: String;
  P: Integer;
begin
  if not RegQueryStringValue(HKLM64, EnvironmentKey, 'Path', Paths) then
    Exit;
  if not PathHas(Paths, Dir) then
    Exit;
  NewPaths := '';
  Rest := Paths;
  while Rest <> '' do
  begin
    P := Pos(';', Rest);
    if P = 0 then
    begin
      Item := Rest;
      Rest := '';
    end else
    begin
      Item := Copy(Rest, 1, P - 1);
      Rest := Copy(Rest, P + 1, Length(Rest));
    end;
    if (Item <> '') and (not SamePath(Item, Dir)) then
    begin
      if NewPaths <> '' then
        NewPaths := NewPaths + ';';
      NewPaths := NewPaths + Item;
    end;
  end;
  if RegWriteExpandStringValue(HKLM64, EnvironmentKey, 'Path', NewPaths) then
    Log('Removed from PATH: ' + Dir)
  else
    Log('Could not write the system PATH.');
end;

// ------------------------------------------------------ former NSIS setup ----

function NsisInstallLocation: String;
begin
  if not RegQueryStringValue(HKLM64, NsisUninstallKey, 'InstallLocation', Result) then
    Result := '';
end;

// Default folder: the folder of a previous NSIS-based install, else Program Files.
function GetDefaultDir(Param: String): String;
begin
  Result := NsisInstallLocation;
  if Result = '' then
    Result := ExpandConstant('{autopf}\{#AppName}');
end;

// Removes what the former NSIS installer registered (its uninstaller, Apps
// entry and Start menu folder), so only one Lanfolk remains in Apps. The
// service is removed by the caller; files in the install folder are replaced
// by this install, and old files in a different folder are deleted here.
procedure RemoveNsisInstall;
var
  Dir, NewDir: String;
  Names: array of String;
  I: Integer;
begin
  Dir := NsisInstallLocation;
  if (Dir = '') and (not RegKeyExists(HKLM64, NsisUninstallKey)) then
    Exit;
  Log('Removing the former NSIS install registration.');
  if Dir <> '' then
  begin
    DeleteFile(AddBackslash(Dir) + 'uninstall.exe');
    NewDir := ExpandConstant('{app}');
    if not SamePath(Dir, NewDir) then
    begin
      Names := ['lanfolk-service.exe', 'lanfolk-cli.exe', 'Lanfolk.exe', 'natprobe.exe',
        'lanprobe.exe', 'wintun.dll', 'wintun-LICENSE.txt', 'LICENSE', 'THIRD_PARTY_NOTICES.md'];
      for I := 0 to GetArrayLength(Names) - 1 do
        DeleteFile(AddBackslash(Dir) + Names[I]);
      RemoveDir(Dir);
    end;
  end;
  DelTree(ExpandConstant('{commonprograms}\{#AppName}'), True, True, True);
  RegDeleteKeyIncludingSubkeys(HKLM64, NsisUninstallKey);
end;

// ------------------------------------------------------------ events ----

function InitializeSetup: Boolean;
begin
  ServiceFailed := False;
  Result := True;
end;

function ServiceReady: Boolean;
begin
  Result := not ServiceFailed;
end;

function GetCustomSetupExitCode: Integer;
begin
  // Same exit code as the former NSIS installer used for a failed service install.
  if ServiceFailed then
    Result := 3
  else
    Result := 0;
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if (CurPageID = wpFinished) and ServiceFailed then
  begin
    WizardForm.FinishedHeadingLabel.Caption := CustomMessage('FinishedServiceFailedHeading');
    WizardForm.FinishedLabel.Caption := CustomMessage('FinishedServiceFailed');
  end;
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  AppDir, Output: String;
  Code: Integer;
begin
  AppDir := ExpandConstant('{app}');

  if CurStep = ssInstall then
  begin
    // Upgrade: stop the old UI and remove the old service before the files are
    // replaced (the service holds wintun.dll open). User data is not touched.
    SetStatus('StatusClosingApp');
    CloseUI;
    SetStatus('StatusRemovingService');
    if not RemoveService(AppDir, Code, Output) then
      Log(Format('Previous service uninstall returned %d: %s', [Code, Output]));
    // A former NSIS install may live in another folder.
    if (NsisInstallLocation <> '') and (not SamePath(NsisInstallLocation, AppDir)) then
      if not RemoveService(NsisInstallLocation, Code, Output) then
        Log(Format('NSIS-era service uninstall returned %d: %s', [Code, Output]));
    RemoveNsisInstall;
  end;

  if CurStep = ssPostInstall then
  begin
    // PATH and auto-start follow the selected tasks on every run, so an
    // upgrade can also switch them off.
    if WizardIsTaskSelected('addtopath') then
      AddToPath(AppDir)
    else
      RemoveFromPath(AppDir);
    if not WizardIsTaskSelected('autostart') then
      RegDeleteValue(HKCU, RunKey, AppDisplayName);

    // Register the auto-start service (also creates the firewall rules and
    // records the installing user as the owner of the control pipe), then
    // start it. Errors are shown.
    SetStatus('StatusInstallingService');
    Code := RunCaptured(AppDir + '\' + ServiceExeName, 'install', Output);
    if Code <> 0 then
    begin
      ServiceFailed := True;
      Notify(FormatMsg('ServiceInstallFailed', [IntToStr(Code), Output, AppDir + '\' + ServiceExeName]), mbError);
      Exit;
    end;

    SetStatus('StatusStartingService');
    Code := RunCaptured(AppDir + '\' + ServiceExeName, 'start', Output);
    if Code <> 0 then
      Notify(FormatMsg('ServiceStartFailed', [IntToStr(Code), Output, AppDir + '\' + ServiceExeName]), mbInformation);
  end;
end;

// ---------------------------------------------------------- uninstall ----

procedure DeleteUserDataIfWanted;
var
  Dir: String;
  WantDelete: Boolean;
begin
  Dir := ExpandConstant('{commonappdata}\{#AppName}');
  if not DirExists(Dir) then
    Exit;
  // The default answer is No, also in silent mode.
  WantDelete := False;
  if not IsSilentRun then
    WantDelete := SuppressibleMsgBox(FormatMsg('DeleteDataPrompt', [Dir]), mbConfirmation,
      MB_YESNO or MB_DEFBUTTON2, IDNO) = IDYES;
  if WantDelete then
  begin
    DelTree(Dir, True, True, True);
    Log(FormatMsg('DataDeleted', [Dir]));
  end else
    Log(FormatMsg('DataKept', [Dir]));
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  AppDir, Output: String;
  Code: Integer;
begin
  AppDir := ExpandConstant('{app}');

  if CurUninstallStep = usUninstall then
  begin
    // 1. Close the app.
    CloseUI;
    // 2. Stop and remove the service, firewall rules and adapter. The data
    //    folder is not touched by this step.
    if not RemoveService(AppDir, Code, Output) then
      Notify(FormatMsg('ServiceUninstallFailed', [IntToStr(Code), Output]), mbInformation);
  end;

  if CurUninstallStep = usPostUninstall then
  begin
    // 3. Files, shortcuts and the Run value are removed by the uninstaller.
    RemoveFromPath(AppDir);
    // 4. Ask about the user data.
    DeleteUserDataIfWanted;
  end;
end;
