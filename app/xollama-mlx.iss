// app/xollama-mlx.iss -- the MLX runtime, downloaded by the installer.
//
// Owner, 2026-10-07: MLX ships on Windows too, downloaded at install. It does
// not fit inside xOllamaSetup.exe: the installer plus the archive would be
// about 2.5 GiB, over GitHub's 2 GiB release-asset cap. Without it a Windows
// install cannot create a model from safetensors (upstream dropped GGUF
// conversion) nor run a model published only for MLX; models with a GGUF
// variant run on opencoti either way (manifest/opencoti_first.go).
//
// The archive is upstream's own Windows MLX build, republished by the fork and
// pinned in llama/runtime-pin-windows-mlx.txt; the release workflow passes its
// URL and sha256 as PKG_MLX_URL / PKG_MLX_SHA256. DownloadTemporaryFile
// refuses any other bytes. MLX on Windows is CUDA only, so by default it is
// fetched only where an NVIDIA GPU is present:
//   /MLX=auto    (default) NVIDIA present -> download
//   /MLX=always  download whatever the GPU (the release job's install check)
//   /MLX=skip    never
//
// LAYOUT (upstream #17956, from v0.40.9): the MLX archive is no longer
// self-contained. It carries two directories under lib/ollama:
//   mlx_cuda_v13\  mlx, mlxc, ollama_xgrammar, libopenblas and the 8 cuDNN 9
//                  DLLs -- mlx.dll loads cuDNN from its own folder
//                  (MLX_CUDNN_BIN_DIR=.), so no cuDNN need be installed on the
//                  machine. This replaces the old -reldir build, retired: as of
//                  v0.40.9 upstream's own mlx.dll embeds "..\cuda_v13" and ships
//                  the cuDNN DLLs here, which is what -reldir existed to do.
//   cuda_v13\      cufft64_12, cusolver64_12, cusparse64_12 -- the CUDA
//                  libraries MLX needs that GGML does not. mlx.dll loads the
//                  rest (cublas64_13, cublasLt64_13, cudart64_13, nvrtc64_130_0,
//                  nvrtc-builtins64_130, nvJitLink_130_0) from ..\cuda_v13 too:
//                  those come from the GGML CUDA-13 backend the FULL installer
//                  lays in lib\ollama\cuda_v13 (xollama.iss [Files], which
//                  excludes only mlx_* and cuda_v12). So the MLX DLLs are
//                  MERGED into that cuda_v13, never replacing it.
//
// A finished install writes the archive's sha256 to
// lib\ollama\mlx_cuda_v13\MLX_ID and the cuda_v13 file names it added to
// lib\ollama\mlx_cuda_v13\MLX_CUDA_V13; a matching, complete runtime is never
// fetched again. The full installer clears lib\ollama ([InstallDelete]), so
// KeepMLX moves a current mlx_cuda_v13 AND its cuda_v13 DLLs out of the way
// first and InstallMLX puts both back.
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
  // The GGML CUDA-13 backend's directory, which the MLX archive's own
  // cuda_v13 DLLs are merged into (see LAYOUT above).
  CudaDir = 'cuda_v13';
  // In MLXDir: the newline-separated cuda_v13 file names the MLX archive
  // supplied, so KeepMLX can stash exactly them and InstallMLX restore them.
  MLXCudaList = 'MLX_CUDA_V13';

function MLXInstalledDir(): string;
begin
  Result := ExpandConstant('{app}\lib\ollama\') + MLXDir;
end;

function CudaInstalledDir(): string;
begin
  Result := ExpandConstant('{app}\lib\ollama\') + CudaDir;
end;

function MLXKeepDir(): string;
begin
  Result := ExpandConstant('{app}\') + MLXDir + '.keep';
end;

// Separate from MLXKeepDir so the kept mlx_cuda_v13 (which MLXKeepDir becomes)
// never ends up with a cuda_v13 subfolder inside it.
function MLXCudaKeepDir(): string;
begin
  Result := ExpandConstant('{app}\') + CudaDir + '-mlx.keep';
end;

function MLXIsCurrent(Dir: string): Boolean;
var
  ID: AnsiString;
begin
  Result := LoadStringFromFile(Dir + '\MLX_ID', ID) and (Trim(String(ID)) = '{#PKG_MLX_SHA256}');
end;

// True when every cuda_v13 file the MLX archive added is present beside the
// GGML backend. Checked with MLXIsCurrent before skipping a fetch, because the
// full installer's [InstallDelete] wipes cuda_v13 independently of mlx_cuda_v13.
function MLXCudaComplete(): Boolean;
var
  Names: TArrayOfString;
  I: Integer;
begin
  Result := False;
  if not LoadStringsFromFile(MLXInstalledDir() + '\' + MLXCudaList, Names) then
    exit;
  for I := 0 to GetArrayLength(Names) - 1 do
    if (Trim(Names[I]) <> '') and not FileExists(CudaInstalledDir() + '\' + Trim(Names[I])) then
      exit;
  Result := True;
end;

// Copies every file (not subdirectory) from the MLX archive's cuda_v13 into the
// installed cuda_v13, beside the GGML backend, and returns their names as a
// newline list for MLX_CUDA_V13.
function CopyCudaFiles(FromDir, IntoDir: string; var NameList: string): Boolean;
var
  FR: TFindRec;
begin
  Result := True;
  NameList := '';
  ForceDirectories(IntoDir);
  if FindFirst(FromDir + '\*', FR) then begin
    try
      repeat
        if (FR.Attributes and FILE_ATTRIBUTE_DIRECTORY) = 0 then begin
          if FileCopy(FromDir + '\' + FR.Name, IntoDir + '\' + FR.Name, False) then
            NameList := NameList + FR.Name + #13#10
          else begin
            Log('MLX: could not copy ' + FR.Name + ' into ' + IntoDir);
            Result := False;
          end;
        end;
      until not FindNext(FR);
    finally
      FindClose(FR);
    end;
  end;
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
// clears lib\ollama: a current runtime is moved aside instead of fetched again.
// Both pieces are stashed -- mlx_cuda_v13 and the cuda_v13 DLLs it added -- so a
// reuse (no re-download) can rebuild a cuda_v13 the wipe will have emptied.
procedure KeepMLX();
var
  Names: TArrayOfString;
  I: Integer;
  Src: string;
begin
  DelTree(MLXKeepDir(), True, True, True);
  DelTree(MLXCudaKeepDir(), True, True, True);
  if not MLXIsCurrent(MLXInstalledDir()) then
    exit;
  if LoadStringsFromFile(MLXInstalledDir() + '\' + MLXCudaList, Names) then begin
    ForceDirectories(MLXCudaKeepDir());
    for I := 0 to GetArrayLength(Names) - 1 do begin
      if Trim(Names[I]) <> '' then begin
        Src := CudaInstalledDir() + '\' + Trim(Names[I]);
        if FileExists(Src) then
          RenameFile(Src, MLXCudaKeepDir() + '\' + Trim(Names[I]));
      end;
    end;
  end;
  if RenameFile(MLXInstalledDir(), MLXKeepDir()) then
    Log('MLX: kept the installed runtime for reuse')
  else
    Log('MLX: could not move the installed runtime aside; it will be downloaded again');
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

// Unpacks lib/ollama/mlx_cuda_v13 AND lib/ollama/cuda_v13 from the archive into
// a staging directory beside the install (same volume, so the move is a
// rename). tar.exe is bsdtar and reads zip (Windows 10 1803+); older systems
// use Expand-Archive (which extracts the whole zip -- only the two dirs are
// used).
function UnpackMLX(Zip, Staging: string): Boolean;
var
  Code: Integer;
begin
  ForceDirectories(Staging);
  if FileExists(ExpandConstant('{sys}\tar.exe')) then
    Result := Exec(ExpandConstant('{sys}\tar.exe'), '-xf "' + Zip + '" -C "' + Staging + '" lib/ollama/' + MLXDir + ' lib/ollama/' + CudaDir, '', SW_HIDE, ewWaitUntilTerminated, Code) and (Code = 0)
  else
    Result := Exec('powershell.exe', '-NoProfile -ExecutionPolicy Bypass -Command "Expand-Archive -LiteralPath ''' + Zip + ''' -DestinationPath ''' + Staging + ''' -Force"', '', SW_HIDE, ewWaitUntilTerminated, Code) and (Code = 0);
  Log(Format('MLX: unpack exit %d', [Code]));
  Result := Result
            and DirExists(Staging + '\lib\ollama\' + MLXDir)
            and DirExists(Staging + '\lib\ollama\' + CudaDir);
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
  Zip, Staging, Names: string;
  Kept: TArrayOfString;
  I: Integer;
begin
  if not MLXWanted() then begin
    DelTree(MLXKeepDir(), True, True, True);
    DelTree(MLXCudaKeepDir(), True, True, True);
    exit;
  end;
  // Reuse a kept runtime: put mlx_cuda_v13 back, then restore its cuda_v13 DLLs
  // into the freshly laid GGML cuda_v13 (the full installer wiped and re-laid
  // it from [Files]).
  if DirExists(MLXKeepDir()) and not DirExists(MLXInstalledDir()) then begin
    if RenameFile(MLXKeepDir(), MLXInstalledDir()) then begin
      Log('MLX: put the kept runtime back');
      if DirExists(MLXCudaKeepDir()) and LoadStringsFromFile(MLXInstalledDir() + '\' + MLXCudaList, Kept) then begin
        ForceDirectories(CudaInstalledDir());
        for I := 0 to GetArrayLength(Kept) - 1 do
          if Trim(Kept[I]) <> '' then
            RenameFile(MLXCudaKeepDir() + '\' + Trim(Kept[I]), CudaInstalledDir() + '\' + Trim(Kept[I]));
      end;
    end;
  end;
  DelTree(MLXKeepDir(), True, True, True);
  DelTree(MLXCudaKeepDir(), True, True, True);
  if MLXIsCurrent(MLXInstalledDir()) and MLXCudaComplete() then begin
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
  // mlx_cuda_v13: replace wholesale.
  DelTree(MLXInstalledDir(), True, True, True);
  if not RenameFile(Staging + '\lib\ollama\' + MLXDir, MLXInstalledDir()) then begin
    DelTree(Staging, True, True, True);
    MLXFailed('the runtime could not be moved into ' + MLXInstalledDir());
    exit;
  end;
  // cuda_v13: MERGE the MLX DLLs beside the GGML CUDA-13 backend, never
  // replacing it (see LAYOUT at the top of this file).
  if not CopyCudaFiles(Staging + '\lib\ollama\' + CudaDir, CudaInstalledDir(), Names) then begin
    DelTree(Staging, True, True, True);
    MLXFailed('the MLX CUDA libraries could not be placed in ' + CudaInstalledDir());
    exit;
  end;
  if not SaveStringToFile(MLXInstalledDir() + '\' + MLXCudaList, Names, False) then begin
    DelTree(Staging, True, True, True);
    MLXFailed('could not record the MLX cuda_v13 file list');
    exit;
  end;
  DelTree(Staging, True, True, True);
  DeleteFile(Zip);
  // Written last: its presence means the runtime is complete.
  if SaveStringToFile(MLXInstalledDir() + '\MLX_ID', '{#PKG_MLX_SHA256}', False) then
    Log(Format('MLX: installed (%d MB downloaded)', [Integer(MLXProgressMax div 1048576)]))
  else
    MLXFailed('could not write ' + MLXInstalledDir() + '\MLX_ID');
end;
