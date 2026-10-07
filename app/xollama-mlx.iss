// app/xollama-mlx.iss -- the MLX runtime, downloaded by the installer.
//
// Owner, 2026-10-07: MLX ships on Windows too, downloaded at install. It does
// not fit inside xOllamaSetup.exe: the installer plus the archive would be
// about 2.5 GiB, over GitHub's 2 GiB release-asset cap. Without it a Windows
// install cannot create a model from safetensors (upstream dropped GGUF
// conversion) nor run a model published only for MLX; models with a GGUF
// variant run on opencoti either way (manifest/opencoti_first.go).
//
// The archive is upstream's ollama-windows-amd64-mlx.zip as the fork
// republishes it, pinned in llama/runtime-pin-windows-mlx.txt; the release
// workflow passes its URL and sha256 as PKG_MLX_URL / PKG_MLX_SHA256.
// DownloadTemporaryFile refuses any other bytes. MLX on Windows is CUDA only,
// so by default it is fetched only where an NVIDIA GPU is present:
//   /MLX=auto    (default) NVIDIA present -> download
//   /MLX=always  download whatever the GPU (the release job's install check)
//   /MLX=skip    never
// A finished install writes the archive's sha256 to
// lib\ollama\mlx_cuda_v13\MLX_ID; a matching one is never fetched again.
// The full installer clears lib\ollama ([InstallDelete]), so KeepMLX moves a
// current MLX out of the way first and InstallMLX puts it back.
//
// A failed download is said, never hidden: a message box (the setup log when
// silent), and the install finishes without MLX -- every GGUF model still
// runs. Running the installer again retries.

#if GetEnv("PKG_MLX_URL") != ""
  #define PKG_MLX_URL GetEnv("PKG_MLX_URL")
  #define PKG_MLX_SHA256 GetEnv("PKG_MLX_SHA256")
#else
  #define PKG_MLX_URL ""
  #define PKG_MLX_SHA256 ""
#endif

const
  MLXDir = 'mlx_cuda_v13';

function MLXInstalledDir(): string;
begin
  Result := ExpandConstant('{app}\lib\ollama\') + MLXDir;
end;

function MLXKeepDir(): string;
begin
  Result := ExpandConstant('{app}\') + MLXDir + '.keep';
end;

function MLXIsCurrent(Dir: string): Boolean;
var
  ID: AnsiString;
begin
  Result := LoadStringFromFile(Dir + '\MLX_ID', ID) and (Trim(String(ID)) = '{#PKG_MLX_SHA256}');
end;

function HasNvidiaGPU(): Boolean;
var
  Locator, Service, Items, Item: Variant;
  I: Integer;
  Name: string;
begin
  Result := False;
  try
    Locator := CreateOleObject('WbemScripting.SWbemLocator');
    Service := Locator.ConnectServer('.', 'root\CIMV2');
    Items := Service.ExecQuery('SELECT Name FROM Win32_VideoController');
    for I := 0 to Items.Count - 1 do begin
      Item := Items.ItemIndex(I);
      if not VarIsNull(Item.Name) then begin
        Name := Item.Name;
        Log('MLX: display adapter ' + Name);
        if Pos('NVIDIA', Uppercase(Name)) > 0 then
          Result := True;
      end;
    end;
  except
    Log('MLX: could not list the display adapters: ' + GetExceptionMessage);
  end;
end;

function MLXWanted(): Boolean;
var
  Mode: string;
begin
  Result := False;
  if '{#PKG_MLX_URL}' = '' then begin
    Log('MLX: this installer was built without an MLX pin; skipping');
    exit;
  end;
  Mode := Lowercase(ExpandConstant('{param:MLX|auto}'));
  if Mode = 'skip' then
    Log('MLX: /MLX=skip')
  else if Mode = 'always' then
    Result := True
  else begin
    Result := HasNvidiaGPU();
    if not Result then
      Log('MLX: no NVIDIA GPU; MLX on Windows needs CUDA, not downloading (/MLX=always forces it)');
  end;
end;

// From PrepareToInstall, after xOllama is stopped and before [InstallDelete]
// clears lib\ollama: a current MLX is moved aside instead of fetched again.
procedure KeepMLX();
begin
  DelTree(MLXKeepDir(), True, True, True);
  if MLXIsCurrent(MLXInstalledDir()) then begin
    if RenameFile(MLXInstalledDir(), MLXKeepDir()) then
      Log('MLX: kept the installed runtime for reuse')
    else
      Log('MLX: could not move the installed runtime aside; it will be downloaded again');
  end;
end;

var
  MLXProgressMax: Int64;

function MLXProgress(const Url, FileName: String; const Progress, ProgressMax: Int64): Boolean;
begin
  if (ProgressMax > 0) and not WizardSilent then begin
    WizardForm.StatusLabel.Caption := Format('Downloading the MLX runtime: %d of %d MB', [Integer(Progress div 1048576), Integer(ProgressMax div 1048576)]);
    WizardForm.ProgressGauge.Max := 1000;
    WizardForm.ProgressGauge.Position := Integer(Progress * 1000 div ProgressMax);
  end;
  MLXProgressMax := ProgressMax;
  Result := True;
end;

// Unpacks lib/ollama/mlx_cuda_v13 from the archive into a staging directory
// beside the install (same volume, so the final step is a rename). tar.exe is
// bsdtar and reads zip (Windows 10 1803+); older systems use Expand-Archive.
function UnpackMLX(Zip, Staging: string): Boolean;
var
  Code: Integer;
begin
  ForceDirectories(Staging);
  if FileExists(ExpandConstant('{sys}\tar.exe')) then
    Result := Exec(ExpandConstant('{sys}\tar.exe'), '-xf "' + Zip + '" -C "' + Staging + '" lib/ollama/' + MLXDir, '', SW_HIDE, ewWaitUntilTerminated, Code) and (Code = 0)
  else
    Result := Exec('powershell.exe', '-NoProfile -ExecutionPolicy Bypass -Command "Expand-Archive -LiteralPath ''' + Zip + ''' -DestinationPath ''' + Staging + ''' -Force"', '', SW_HIDE, ewWaitUntilTerminated, Code) and (Code = 0);
  Log(Format('MLX: unpack exit %d', [Code]));
  Result := Result and DirExists(Staging + '\lib\ollama\' + MLXDir);
end;

procedure MLXFailed(Why: string);
begin
  Log('MLX: NOT installed: ' + Why);
  if not WizardSilent then
    MsgBox('The MLX runtime could not be installed:' + #13#10 + Why + #13#10#13#10 +
           'xOllama is installed and runs every GGUF model. Without MLX it cannot create a model from safetensors ' +
           'or run a model published only for MLX. Run the installer again to retry.', mbError, MB_OK);
end;

// After the files are installed, by both installers.
procedure InstallMLX();
var
  Zip, Staging: string;
begin
  if not MLXWanted() then begin
    DelTree(MLXKeepDir(), True, True, True);
    exit;
  end;
  if DirExists(MLXKeepDir()) and not DirExists(MLXInstalledDir()) then
    if RenameFile(MLXKeepDir(), MLXInstalledDir()) then
      Log('MLX: put the kept runtime back');
  DelTree(MLXKeepDir(), True, True, True);
  if MLXIsCurrent(MLXInstalledDir()) then begin
    Log('MLX: runtime {#PKG_MLX_SHA256} already installed');
    exit;
  end;
  Log('MLX: downloading {#PKG_MLX_URL}');
  try
    DownloadTemporaryFile('{#PKG_MLX_URL}', 'xollama-mlx.zip', '{#PKG_MLX_SHA256}', @MLXProgress);
  except
    MLXFailed('the download failed or its sha256 is not the pinned one: ' + GetExceptionMessage);
    exit;
  end;
  if not WizardSilent then
    WizardForm.StatusLabel.Caption := 'Unpacking the MLX runtime...';
  Zip := ExpandConstant('{tmp}\xollama-mlx.zip');
  Staging := ExpandConstant('{app}\lib\ollama\.mlx-staging');
  DelTree(Staging, True, True, True);
  if not UnpackMLX(Zip, Staging) then begin
    DelTree(Staging, True, True, True);
    MLXFailed('the archive could not be unpacked (see the setup log)');
    exit;
  end;
  DelTree(MLXInstalledDir(), True, True, True);
  if not RenameFile(Staging + '\lib\ollama\' + MLXDir, MLXInstalledDir()) then begin
    DelTree(Staging, True, True, True);
    MLXFailed('the runtime could not be moved into ' + MLXInstalledDir());
    exit;
  end;
  DelTree(Staging, True, True, True);
  DeleteFile(Zip);
  // Written last: its presence means the directory is complete.
  if SaveStringToFile(MLXInstalledDir() + '\MLX_ID', '{#PKG_MLX_SHA256}', False) then
    Log(Format('MLX: installed (%d MB downloaded)', [Integer(MLXProgressMax div 1048576)]))
  else
    MLXFailed('could not write ' + MLXInstalledDir() + '\MLX_ID');
end;
