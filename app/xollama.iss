; Inno Setup Installer for xOllama
;
; To build the installer use the build script invoked from the top of the source tree
; 
; powershell -ExecutionPolicy Bypass -File .\scripts\build_windows.ps


#define MyAppName "xOllama"
#if GetEnv("PKG_VERSION") != ""
  #define MyAppVersion GetEnv("PKG_VERSION")
#else
  #define MyAppVersion "0.0.0"
#endif
#if GetEnv("PKG_PAYLOAD_ID") != ""
  #define PKG_PAYLOAD_ID GetEnv("PKG_PAYLOAD_ID")
#else
  #define PKG_PAYLOAD_ID "unset"
#endif
#define MyAppPublisher "ManniX"
#define MyAppURL "https://github.com/mann1x/xollama"
#define MyAppExeName "xOllama app.exe"
#define MyIcon ".\assets\app.ico"

[Setup]
; NOTE: The value of AppId uniquely identifies this application. Do not use the same AppId value in installers for other applications.
; (To generate a new GUID, click Tools | Generate GUID inside the IDE.)
AppId={{F6F806B4-09B2-43BA-8413-B1D52561CA60}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
VersionInfoVersion={#MyAppVersion}
;AppVerName={#MyAppName} {#MyAppVersion}
AppPublisher={#MyAppPublisher}

; The Add/Remove Programs entry is the surface an EXTERNAL updater correlates
; on. winget matches a manifest with no ProductCode by DisplayName +
; Publisher, which is how a stock-ollama manifest once drove the Microsoft
; Store to "upgrade" a fork install over the top of itself -- nothing inside
; the app could see it or stop it. So this entry must never read like Ollama's:
; keep UninstallDisplayName and AppPublisher distinct from "Ollama" /
; "Ollama Inc." and keep AppId a GUID of our own.
UninstallDisplayName={#MyAppName}

; Written into the setup binary's version resource, so Explorer's Properties
; tab, SmartScreen's prompt and any future payload-identity check all name the
; product rather than showing a blank publisher.
VersionInfoProductName={#MyAppName}
VersionInfoProductTextVersion={#MyAppVersion}
VersionInfoCompany={#MyAppPublisher}
VersionInfoDescription={#MyAppName} Setup
VersionInfoCopyright={#MyAppPublisher}
AppCopyright={#MyAppPublisher}

AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}
AppUpdatesURL={#MyAppURL}
ArchitecturesAllowed=x64compatible arm64
ArchitecturesInstallIn64BitMode=x64compatible arm64
DefaultDirName={localappdata}\Programs\{#MyAppName}
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
; CORE builds the executables-only installer: same script, without anything
; under lib\ollama. That payload is ~1.5 GB against 36 MB of Go binary, so a
; release that changed no native code can be installed with two thirds less
; downloaded. It is an UPDATE ONLY -- PayloadRefusal below refuses it on a
; machine with no matching payload, because an install with no engine is worse
; than no install.
#ifdef CORE
OutputBaseFilename="xOllamaUpdate"
#else
OutputBaseFilename="xOllamaSetup"
#endif
SetupIconFile={#MyIcon}
UninstallDisplayIcon={uninstallexe}
Compression=lzma2/ultra64
LZMAUseSeparateProcess=yes
LZMANumBlockThreads=8
SolidCompression=yes
WizardStyle=modern
ChangesEnvironment=yes
OutputDir=..\dist\

; Disable logging once everything's battle tested
; Filename will be %TEMP%\Setup Log*.txt
SetupLogging=yes
CloseApplications=no
RestartApplications=no
RestartIfNeededByRun=no

; https://jrsoftware.org/ishelp/index.php?topic=setup_wizardimagefile
WizardSmallImageFile=.\assets\setup.bmp

; xOllama requires Windows 10 22H2 or newer for proper unicode rendering
; TODO: consider setting this to 10.0.19045
MinVersion=10.0.10240

; First release that supports WinRT UI Composition for win32 apps
; MinVersion=10.0.17134
; First release with XAML Islands - possible UI path forward
; MinVersion=10.0.18362

; quiet...
DisableDirPage=yes
DisableFinishedPage=yes
DisableReadyMemo=yes
DisableReadyPage=yes
DisableStartupPrompt=yes

; TODO - percentage can't be set less than 100, so how to make it shorter?
; WizardSizePercent=100,80

#if GetEnv("KEY_CONTAINER")
SignTool=MySignTool
SignedUninstaller=yes
#endif

SetupMutex=xOllamaSetupMutex

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[LangOptions]
DialogFontSize=12

[Files]
; xollama-stop.ps1 stops xOllama's own processes and nothing else: it runs from
; PrepareToInstall (extracted to {tmp}) before any file is replaced, and from
; [UninstallRun] (installed below) before any file is removed.
Source: ".\xollama-stop.ps1"; Flags: dontcopy
Source: ".\xollama-stop.ps1"; DestDir: "{app}"; Flags: ignoreversion
#if FileExists("..\dist\windows-xollama-app-amd64.exe")
Source: "..\dist\windows-xollama-app-amd64.exe"; DestDir: "{app}"; DestName: "{#MyAppExeName}" ;Check: not IsArm64();  Flags: ignoreversion 64bit; BeforeInstall: TaskKill('{#MyAppExeName}')
Source: "..\dist\windows-amd64\xollama.exe"; DestDir: "{app}"; Check: not IsArm64(); Flags: ignoreversion 64bit; BeforeInstall: TaskKill('xollama.exe')
; cuda_v12 is excluded and shipped as the separate legacy add-on zip. It is ~1.1 GB
; and only serves compute 5.x/6.x/7.0 cards (Maxwell, Pascal, Volta); cuda_v13 and
; opencoti-llamafile both floor at 7.5. Keeping it out is what holds this installer
; under GitHub's 2 GiB release-asset cap now that the engine ships inside it.
#ifndef CORE
Source: "..\dist\windows-amd64\lib\ollama\*"; Excludes: "\mlx_*\*,\cuda_v12\*"; DestDir: "{app}\lib\ollama\"; Check: not IsArm64(); Flags: ignoreversion 64bit recursesubdirs
#endif
#endif

; For local development, rely on binary compatibility at runtime since we can't cross compile
#if FileExists("..\dist\windows-xollama-app-arm64.exe")
Source: "..\dist\windows-xollama-app-arm64.exe"; DestDir: "{app}"; DestName: "{#MyAppExeName}" ;Check: IsArm64();  Flags: ignoreversion 64bit; BeforeInstall: TaskKill('{#MyAppExeName}')
#else 
Source: "..\dist\windows-xollama-app-amd64.exe"; DestDir: "{app}"; DestName: "{#MyAppExeName}" ;Check: IsArm64();  Flags: ignoreversion 64bit; BeforeInstall: TaskKill('{#MyAppExeName}')
#endif

#if FileExists("..\dist\windows-arm64\xollama.exe")
Source: "..\dist\windows-arm64\xollama.exe"; DestDir: "{app}"; Check: IsArm64(); Flags: ignoreversion 64bit; BeforeInstall: TaskKill('xollama.exe')
#endif
#if DirExists("..\dist\windows-arm64\lib\ollama")
#ifndef CORE
Source: "..\dist\windows-arm64\lib\ollama\*"; DestDir: "{app}\lib\ollama\"; Check: IsArm64(); Flags: ignoreversion 64bit recursesubdirs
#endif
#endif

Source: ".\assets\app.ico"; DestDir: "{app}"; Flags: ignoreversion

#ifndef CORE
; The digest of the payload this installer carries. app/updater/fork.go reads it
; back to decide whether the next update needs the whole installer or only the
; executables. It lives INSIDE the payload so it cannot outlive it.
Source: "..\dist\payload-id.txt"; DestDir: "{app}\lib\ollama"; DestName: "PAYLOAD_ID"; Flags: ignoreversion
#endif

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; IconFilename: "{app}\app.ico"
Name: "{app}\lib\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; IconFilename: "{app}\app.ico"
Name: "{userprograms}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; IconFilename: "{app}\app.ico"

[InstallDelete]
Type: files; Name: "{%LOCALAPPDATA}\xOllama\updates"

[Run]
#ifdef CORE
Filename: "{cmd}"; Parameters: "/C set PATH={app};%PATH% & ""{app}\{#MyAppExeName}"""; Flags: postinstall nowait runhidden
#else
; AppRunParams (xollama-setup-pages.iss) also hands the first launch the
; XOLLAMA_HOST the port page wrote, which this process's environment predates.
Filename: "{cmd}"; Parameters: "{code:AppRunParams}"; Flags: postinstall nowait runhidden
#endif

[UninstallRun]
; Filename: "{cmd}"; Parameters: "/C ""taskkill /im ''{#MyAppExeName}'' /f /t"; Flags: runhidden
; Filename: "{cmd}"; Parameters: "/C ""taskkill /im xollama.exe /f /t"; Flags: runhidden
; Stops the tray app, the server and the engines and runners they started, and
; waits for them to exit. Not `taskkill /im llama-server.exe`: that also stopped
; a stock Ollama's runners, and never reached the opencoti engine.
Filename: "powershell.exe"; Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\xollama-stop.ps1"" -App ""{app}"""; Flags: runhidden; RunOnceId: "StopXollama"

[UninstallDelete]
Type: filesandordirs; Name: "{%TEMP}\xollama*"
Type: filesandordirs; Name: "{%LOCALAPPDATA}\xOllama"
Type: filesandordirs; Name: "{%LOCALAPPDATA}\Programs\xOllama"
; NOTE: ~/.ollama is shared with a stock ollama install, which xollama is
; designed to sit beside, so uninstalling xollama must not delete it.
Type: filesandordirs; Name: "{userstartup}\{#MyAppName}.lnk"
; NOTE: if the user has a custom OLLAMA_MODELS it will be preserved

[InstallDelete]
Type: filesandordirs; Name: "{%TEMP}\xollama*"
#ifndef CORE
; Only the full installer may clear the payload: it is about to lay a new one
; down. The core installer does not carry one, so deleting it would leave a
; machine with no engine at all.
Type: filesandordirs; Name: "{app}\lib\ollama"
#endif

[Messages]
WizardReady=xOllama
ReadyLabel1=%nLet's get you up and running with your own large language models.
SetupAppRunningError=Another xOllama installer is running.%n%nPlease cancel or finish the other installer, then click OK to continue with this install, or Cancel to exit.


;FinishedHeadingLabel=Run your first model
;FinishedLabel=%nRun this command in a PowerShell or cmd terminal.%n%n%n    xollama run llama3.2
;ClickFinish=%n

[Registry]
Root: HKCU; Subkey: "Environment"; \
    ValueType: expandsz; ValueName: "Path"; ValueData: "{olddata};{app}"; \
    Check: NeedsAddPath('{app}')
; Our own URL protocol, unconditionally.
Root: HKCU; Subkey: "Software\Classes\xollama"; ValueType: string; ValueName: ""; ValueData: "URL:xOllama Protocol"; Flags: uninsdeletekey
Root: HKCU; Subkey: "Software\Classes\xollama"; ValueType: string; ValueName: "URL Protocol"; ValueData: ""; Flags: uninsdeletekey
Root: HKCU; Subkey: "Software\Classes\xollama\shell\open\command"; ValueType: string; ValueName: ""; ValueData: """{app}\{#MyAppExeName}"" ""%1"""; Flags: uninsdeletekey

; ollama://, and ONLY when nobody owns it. This used to be registered
; unconditionally with uninsdeletekey, which was wrong in both directions: it
; took the scheme from a stock ollama install, and uninstalling xollama then
; DELETED the key, leaving a working ollama whose links opened nothing.
;
; It cannot simply be dropped either. Sign-in opens
; https://ollama.com/connect?...&launch=true and OLLAMA.COM picks the scheme it
; redirects to -- ollama://connect, which we do not control. On a machine with
; no stock ollama, refusing the scheme outright would leave that redirect with
; no handler at all and silently break sign-in.
;
; So: claim it only if it is unclaimed. OllamaSchemeUnclaimed checks HKCR, which
; is the merged HKLM+HKCU view, so a stock install in either hive keeps it. The
; Check gates the recorded uninstall action too -- a row that was never
; installed is never removed -- so this can no longer delete somebody else's.
Root: HKCU; Subkey: "Software\Classes\ollama"; ValueType: string; ValueName: ""; ValueData: "URL:xOllama Protocol"; Check: OllamaSchemeUnclaimed; Flags: uninsdeletekey
Root: HKCU; Subkey: "Software\Classes\ollama"; ValueType: string; ValueName: "URL Protocol"; ValueData: ""; Check: OllamaSchemeUnclaimed; Flags: uninsdeletekey
Root: HKCU; Subkey: "Software\Classes\ollama\shell\open\command"; ValueType: string; ValueName: ""; ValueData: """{app}\{#MyAppExeName}"" ""%1"""; Check: OllamaSchemeUnclaimed; Flags: uninsdeletekey

#ifndef CORE
; The port and KV cache pages' choices (xollama-setup-pages.iss). Only a variable this
; install creates is removed on uninstall; one the user already had is
; rewritten from code and outlives xOllama.
Root: HKCU; Subkey: "Environment"; ValueType: expandsz; ValueName: "XOLLAMA_HOST"; ValueData: "{code:HostValue}"; Check: HostIsNew; Flags: uninsdeletevalue
Root: HKCU; Subkey: "Environment"; ValueType: expandsz; ValueName: "XOLLAMA_K_CACHE_TYPE"; ValueData: "{code:KVValue|K}"; Check: KVIsNew('K'); Flags: uninsdeletevalue
Root: HKCU; Subkey: "Environment"; ValueType: expandsz; ValueName: "XOLLAMA_V_CACHE_TYPE"; ValueData: "{code:KVValue|V}"; Check: KVIsNew('V'); Flags: uninsdeletevalue
Root: HKCU; Subkey: "Environment"; ValueType: expandsz; ValueName: "XOLLAMA_KV_CACHE_TYPE"; ValueData: "{code:KVValue|L}"; Check: KVIsNew('L'); Flags: uninsdeletevalue
#endif

[Code]

#ifdef CORE
// The executables-only installer is an UPDATE, not an install. It carries no
// lib\ollama, so running it where none is present -- or where the one present
// was built from different native code -- leaves an xollama that cannot load a
// model. Both cases are refused here rather than discovered at first run.
//
// PAYLOAD_ID is written by the full installer and holds the digest of the
// payload it laid down; PKG_PAYLOAD_ID is the digest of the payload the release
// this update belongs to was built with. app/updater/fork.go makes the same
// comparison before downloading, so reaching this message means something
// bypassed the updater -- a hand-run installer, or a release whose assets were
// mixed.
//
// PayloadRefusal is asked from PrepareToInstall, never from InitializeSetup:
// {app} does not exist that early, and expanding it there ended every run of
// this installer with a runtime error (v0.35.1-xollama, 2026-10-05).
function PayloadRefusal(): String;
var
  InstalledID: AnsiString;
  MarkerPath: string;
begin
  Result := '';
  MarkerPath := ExpandConstant('{app}\lib\ollama\PAYLOAD_ID');
  if not FileExists(MarkerPath) then begin
    Result := 'This is the update-only installer for {#MyAppName}.' + #13#10#13#10 +
              'It does not contain the inference engine, and no existing installation was found at' + #13#10 +
              ExpandConstant('{app}') + #13#10#13#10 +
              'Download xOllamaSetup.exe instead.';
    exit;
  end;
  if not LoadStringFromFile(MarkerPath, InstalledID) then begin
    // No continuation line may start with '#': ISPP reads it as a directive.
    Result := 'Could not read the installed engine payload marker at' + #13#10 + MarkerPath + #13#10#13#10 +
              'Download xOllamaSetup.exe instead.';
    exit;
  end;
  if Trim(String(InstalledID)) <> '{#PKG_PAYLOAD_ID}' then
    Result := 'This update was built against a different inference engine than the one installed.' + #13#10#13#10 +
              'Download xOllamaSetup.exe instead.';
end;
#endif

// OllamaSchemeUnclaimed is true when no application has registered ollama://.
// HKEY_CLASSES_ROOT is the merged HKLM + HKCU view, so this sees a stock
// ollama installed for the machine or for the user, either way.
function OllamaSchemeUnclaimed(): Boolean;
begin
  Result := not RegKeyExists(HKEY_CLASSES_ROOT, 'ollama');
  if not Result then
    Log('ollama:// is already registered; leaving it alone');
end;

function NeedsAddPath(Param: string): boolean;
var
  OrigPath: string;
begin
  if not RegQueryStringValue(HKEY_CURRENT_USER,
    'Environment',
    'Path', OrigPath)
  then begin
    Result := True;
    exit;
  end;
  { look for the path with leading and trailing semicolon }
  { Pos() returns 0 if not found }
  Result := Pos(';' + ExpandConstant(Param) + ';', ';' + OrigPath + ';') = 0;
end;

function GetDirSize(Path: String): Int64;
var
  FindRec: TFindRec;
  FilePath: string;
  Size: Int64;
begin
  if FindFirst(Path + '\*', FindRec) then begin
    Result := 0;
    try
      repeat
        if (FindRec.Name <> '.') and (FindRec.Name <> '..') then begin
          FilePath := Path + '\' + FindRec.Name;
          if (FindRec.Attributes and FILE_ATTRIBUTE_DIRECTORY) <> 0 then begin
            Size := GetDirSize(FilePath);
          end else begin
            Size := Int64(FindRec.SizeHigh) shl 32 + FindRec.SizeLow;
          end;
          Result := Result + Size;
        end;
      until not FindNext(FindRec);
    finally
      FindClose(FindRec);
    end;
  end else begin
    Log(Format('Failed to list %s', [Path]));
    Result := -1;
  end;
end;

var
  DeleteModelsChecked: Boolean;
  ModelsDir: string;

procedure InitializeUninstallProgressForm();
var
  UninstallPage: TNewNotebookPage;
  UninstallButton: TNewButton;
  DeleteModelsCheckbox: TNewCheckBox;
  OriginalPageNameLabel: string;
  OriginalPageDescriptionLabel: string;
  OriginalCancelButtonEnabled: Boolean;
  OriginalCancelButtonModalResult: Integer;
  ctrl: TWinControl;
  ModelDirA: AnsiString;
  ModelsSize: Int64;
begin
  if not UninstallSilent then begin
    ctrl := UninstallProgressForm.CancelButton;
    UninstallButton := TNewButton.Create(UninstallProgressForm);
    UninstallButton.Parent := UninstallProgressForm;
    UninstallButton.Left := ctrl.Left - ctrl.Width - ScaleX(10);
    UninstallButton.Top := ctrl.Top;
    UninstallButton.Width := ctrl.Width;
    UninstallButton.Height := ctrl.Height;
    UninstallButton.TabOrder := ctrl.TabOrder;
    UninstallButton.Caption := 'Uninstall';
    UninstallButton.ModalResult := mrOK;    
    UninstallProgressForm.CancelButton.TabOrder := UninstallButton.TabOrder + 1;
    UninstallPage := TNewNotebookPage.Create(UninstallProgressForm);
    UninstallPage.Notebook := UninstallProgressForm.InnerNotebook;
    UninstallPage.Parent := UninstallProgressForm.InnerNotebook;
    UninstallPage.Align := alClient;
    UninstallProgressForm.InnerNotebook.ActivePage := UninstallPage;

    ctrl := UninstallProgressForm.StatusLabel;
    with TNewStaticText.Create(UninstallProgressForm) do begin
      Parent := UninstallPage;
      Top := ctrl.Top;
      Left := ctrl.Left;
      Width := ctrl.Width;
      Height := ctrl.Height;
      AutoSize := False;
      ShowAccelChar := False;
      Caption := '';
    end;

    if (DirExists(GetEnv('USERPROFILE') + '\.ollama\models\blobs')) then begin
      ModelsDir := GetEnv('USERPROFILE') + '\.ollama\models';
      ModelsSize := GetDirSize(ModelsDir);
    end;

    DeleteModelsCheckbox := TNewCheckBox.Create(UninstallProgressForm);
    DeleteModelsCheckbox.Parent := UninstallPage;
    DeleteModelsCheckbox.Top := ctrl.Top + ScaleY(30);
    DeleteModelsCheckbox.Left := ctrl.Left;
    DeleteModelsCheckbox.Width := ScaleX(300);
    if ModelsSize > 1024*1024*1024 then begin
      DeleteModelsCheckbox.Caption := 'Remove models (' + IntToStr(ModelsSize/(1024*1024*1024)) + ' GB) from ' + ModelsDir + ' - shared with Ollama if installed';
    end else if ModelsSize > 1024*1024 then begin
      DeleteModelsCheckbox.Caption := 'Remove models (' + IntToStr(ModelsSize/(1024*1024)) + ' MB) from ' + ModelsDir + ' - shared with Ollama if installed';
    end else begin
      DeleteModelsCheckbox.Caption := 'Remove models from ' + ModelsDir + ' - shared with Ollama if installed';
    end;
    // NOT pre-ticked. ~/.ollama/models is the SAME directory a stock ollama
    // uses, and xollama exists to sit beside one -- the [UninstallDelete]
    // section above says so and deliberately leaves the directory alone. A
    // ticked-by-default box six lines later would delete the other product's
    // models on the way out, from a dialog whose default action is "Uninstall".
    // Ticking it is a decision the user has to make, not one to make for them.
    DeleteModelsCheckbox.Checked := False;

    OriginalPageNameLabel := UninstallProgressForm.PageNameLabel.Caption;
    OriginalPageDescriptionLabel := UninstallProgressForm.PageDescriptionLabel.Caption;
    OriginalCancelButtonEnabled := UninstallProgressForm.CancelButton.Enabled;
    OriginalCancelButtonModalResult := UninstallProgressForm.CancelButton.ModalResult;

    UninstallProgressForm.PageNameLabel.Caption := '';
    UninstallProgressForm.PageDescriptionLabel.Caption := '';
    UninstallProgressForm.CancelButton.Enabled := True;
    UninstallProgressForm.CancelButton.ModalResult := mrCancel;

    if UninstallProgressForm.ShowModal = mrCancel then Abort;

    UninstallButton.Visible := False;   
    UninstallProgressForm.PageNameLabel.Caption := OriginalPageNameLabel;
    UninstallProgressForm.PageDescriptionLabel.Caption := OriginalPageDescriptionLabel;
    UninstallProgressForm.CancelButton.Enabled := OriginalCancelButtonEnabled;
    UninstallProgressForm.CancelButton.ModalResult := OriginalCancelButtonModalResult;

    UninstallProgressForm.InnerNotebook.ActivePage := UninstallProgressForm.InstallingPage;

    if DeleteModelsCheckbox.Checked then begin
      DeleteModelsChecked:=True;
    end else begin
      DeleteModelsChecked:=False;
    end;
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usDone then begin
    if DeleteModelsChecked then begin
      Log('user requested model cleanup');
      if (VarIsEmpty(ModelsDir)) then begin
        Log('cleaning up home directory models')
        DelTree(GetEnv('USERPROFILE') + '\.ollama\models', True, True, True);
      end else begin
        Log('cleaning up custom directory models ' + ModelsDir)
        DelTree(ModelsDir + '\blobs', True, True, True);
        DelTree(ModelsDir + '\manifests', True, True, True);
      end;
    end else begin
      Log('user requested to preserve model dir');
    end;
  end;
end;

// StopXollama stops xOllama's own processes (xollama-stop.ps1) and waits for
// them to exit. It never stops a stock Ollama beside it.
procedure StopXollama(Script: String);
var
  ResultCode: Integer;
begin
  if not Exec('powershell.exe', '-NoProfile -ExecutionPolicy Bypass -File "' + Script + '" -App "' + ExpandConstant('{app}') + '"',
              '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
    Log('xollama-stop.ps1 could not be started')
  else
    Log('xollama-stop.ps1 exited ' + IntToStr(ResultCode));
end;

// Runs before any file is replaced: the engine and the runners live under
// lib\ollama, which the installer overwrites after the executables.
function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
#ifdef CORE
  // Refused before anything is stopped: a machine this update is not for
  // keeps its running xOllama.
  Result := PayloadRefusal();
  if Result <> '' then begin
    Log('update refused: ' + Result);
    exit;
  end;
#endif
  ExtractTemporaryFile('xollama-stop.ps1');
  StopXollama(ExpandConstant('{tmp}\xollama-stop.ps1'));
  Result := '';
end;

// Kept as each executable's BeforeInstall: the stop already ran in
// PrepareToInstall, so this only catches a process started since.
procedure TaskKill(FileName: String);
begin
  StopXollama(ExpandConstant('{tmp}\xollama-stop.ps1'));
end;

#ifndef CORE
#include "xollama-setup-pages.iss"
#endif
