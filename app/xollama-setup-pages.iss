// The interactive setup pages of the full installer, #included into
// xollama.iss's [Code]. docs/features/windows-installer.md describes them.
//
//   1. Ollama found   -- a stock (or forked) Ollama is installed: uninstall it
//                        first, or keep it and run beside it.
//   2. Port           -- 22434, 11434 or custom; 11434 is greyed out while an
//                        Ollama stays installed, since it listens there.
//   3. API key        -- optional; generate, paste, copy; empty skips.
//   4. KV cache       -- server-wide K and V types for opencoti, and the one
//                        type stock llama.cpp falls back to.
//
// None of this runs in a silent install -- the updater's path -- because a
// silent install shows no pages and every choice below defaults to "change
// nothing": no uninstall, no variable written, no key written.

const
  UninstallRoot = 'Software\Microsoft\Windows\CurrentVersion\Uninstall';
  XollamaAppIdKey = '{F6F806B4-09B2-43BA-8413-B1D52561CA60}_is1';
  XollamaDefaultPort = '22434';
  OllamaDefaultPort = '11434';
  MinAPIKeyLength = 16;

var
  OllamaFound: Boolean;
  OllamaName, OllamaVersion, OllamaUninstaller, OllamaDir: string;
  OllamaRemoved: Boolean;

  OllamaPage: TWizardPage;
  OllamaRemoveRadio, OllamaKeepRadio: TNewRadioButton;

  PortPage: TWizardPage;
  PortXollamaRadio, PortOllamaRadio, PortCustomRadio: TNewRadioButton;
  PortCustomEdit: TNewEdit;
  ExistingHost: string;   // XOLLAMA_HOST in HKCU\Environment before this install
  HostScheme, HostName: string;
  ChosenPort: string;     // '' = leave XOLLAMA_HOST as it is

  KeyPage: TWizardPage;
  KeyEdit: TNewEdit;
  KeyExisting: Boolean;
  ChosenKey: string;      // '' = leave the key as it is

// ---------------------------------------------------------------- Ollama

// A product is Ollama when its Add/Remove Programs name is "Ollama" or starts
// with "Ollama " -- "Ollama version 0.x", "Ollama think-budget". That leaves
// out "xOllama" and tools like "ollama-grid-search".
function IsOllamaName(Name: string): Boolean;
begin
  Name := Lowercase(Trim(Name));
  Result := (Name = 'ollama') or (Pos('ollama ', Name) = 1);
end;

procedure ScanUninstallRoot(Root: Integer);
var
  Names: TArrayOfString;
  I: Integer;
  Key, Name, Value: string;
begin
  if OllamaFound or not RegGetSubkeyNames(Root, UninstallRoot, Names) then
    exit;
  for I := 0 to GetArrayLength(Names) - 1 do begin
    if CompareText(Names[I], XollamaAppIdKey) = 0 then
      continue;
    Key := UninstallRoot + '\' + Names[I];
    if not RegQueryStringValue(Root, Key, 'DisplayName', Name) or not IsOllamaName(Name) then
      continue;
    OllamaFound := True;
    OllamaName := Name;
    if RegQueryStringValue(Root, Key, 'DisplayVersion', Value) then
      OllamaVersion := Value;
    if RegQueryStringValue(Root, Key, 'UninstallString', Value) then
      OllamaUninstaller := RemoveQuotes(Value);
    if RegQueryStringValue(Root, Key, 'InstallLocation', Value) then
      OllamaDir := RemoveBackslashUnlessRoot(Value);
    Log('found ' + Name + ' ' + OllamaVersion + ' uninstaller ' + OllamaUninstaller);
    exit;
  end;
end;

procedure DetectOllama();
begin
  OllamaFound := False;
  ScanUninstallRoot(HKEY_CURRENT_USER);
  ScanUninstallRoot(HKEY_LOCAL_MACHINE_64);
  ScanUninstallRoot(HKEY_LOCAL_MACHINE_32);
  // An Ollama copied into place by hand has no uninstaller entry. It is still
  // an Ollama on this machine, so it is still worth the warning; it just
  // cannot be removed from here.
  if not OllamaFound and FileExists(ExpandConstant('{localappdata}\Programs\Ollama\ollama.exe')) then begin
    OllamaFound := True;
    OllamaName := 'Ollama';
    OllamaDir := ExpandConstant('{localappdata}\Programs\Ollama');
    Log('found Ollama at ' + OllamaDir + ' with no uninstaller entry');
  end;
end;

// Ollama's uninstaller deletes %LOCALAPPDATA%\Ollama, which holds its desktop
// app's settings and chats. Keep a copy before it runs -- in ~/.ollama, which
// neither uninstaller removes (xOllama's deletes %LOCALAPPDATA%\xOllama).
procedure BackupOllamaAppData();
var
  Src, Dst: string;
  RC: Integer;
begin
  Src := ExpandConstant('{localappdata}\Ollama');
  if not DirExists(Src) then
    exit;
  Dst := ExpandConstant('{%USERPROFILE}\.ollama\ollama-app-backup-') + GetDateTimeString('yyyymmdd-hhnnss', #0, #0);
  ForceDirectories(Dst);
  Exec(ExpandConstant('{sys}\robocopy.exe'), '"' + Src + '" "' + Dst + '" /E /R:1 /W:1 /NP /NJH /NJS', '', SW_HIDE, ewWaitUntilTerminated, RC);
  Log(Format('backed up %s to %s (robocopy %d)', [Src, Dst, RC]));
end;

// Ollama's uninstaller is an Inno Setup one: run it /SILENT, which is also
// what keeps the models -- its interactive dialog ticks "Remove models" by
// default, silent mode never deletes them. It re-launches itself from %TEMP%
// and returns at once, so wait for it to delete its own executable.
function UninstallOllama(): Boolean;
var
  RC, Waited: Integer;
begin
  Result := False;
  BackupOllamaAppData();
  WizardForm.NextButton.Enabled := False;
  try
    if not Exec(OllamaUninstaller, '/SILENT /SUPPRESSMSGBOXES /NORESTART', '', SW_SHOW, ewWaitUntilTerminated, RC) then begin
      MsgBox('Could not start the Ollama uninstaller:' + #13#10 + OllamaUninstaller + #13#10#13#10 + SysErrorMessage(RC), mbError, MB_OK);
      exit;
    end;
    Waited := 0;
    while FileExists(OllamaUninstaller) and (Waited < 180000) do begin
      Sleep(500);
      Waited := Waited + 500;
    end;
  finally
    WizardForm.NextButton.Enabled := True;
  end;
  DetectOllama();
  Result := not OllamaFound;
  if not Result then
    MsgBox('Ollama is still installed. Uninstall it from Windows Settings, then click Next again,' + #13#10 +
           'or choose to keep it and run xOllama beside it.', mbError, MB_OK);
end;

procedure CreateOllamaPage(AfterID: Integer);
var
  Text: TNewStaticText;
  Found: string;
begin
  Found := OllamaName;
  if OllamaVersion <> '' then
    Found := Found + ' ' + OllamaVersion;
  if OllamaDir <> '' then
    Found := Found + ' in ' + OllamaDir;
  OllamaPage := CreateCustomPage(AfterID, 'Ollama is installed',
    'xOllama can replace it or run beside it.');

  Text := TNewStaticText.Create(OllamaPage);
  Text.Parent := OllamaPage.Surface;
  Text.WordWrap := True;
  Text.Width := OllamaPage.SurfaceWidth;
  Text.Caption := 'Found: ' + Found + #13#10#13#10 +
    'Both read the same models, so nothing is downloaded again either way.';

  OllamaRemoveRadio := TNewRadioButton.Create(OllamaPage);
  OllamaRemoveRadio.Parent := OllamaPage.Surface;
  OllamaRemoveRadio.Top := Text.Top + Text.Height + ScaleY(16);
  OllamaRemoveRadio.Width := OllamaPage.SurfaceWidth;
  OllamaRemoveRadio.Caption := 'Uninstall Ollama first (recommended)';
  OllamaRemoveRadio.Checked := OllamaUninstaller <> '';
  OllamaRemoveRadio.Enabled := OllamaUninstaller <> '';

  with TNewStaticText.Create(OllamaPage) do begin
    Parent := OllamaPage.Surface;
    Left := ScaleX(18);
    Top := OllamaRemoveRadio.Top + OllamaRemoveRadio.Height + ScaleY(2);
    Width := OllamaPage.SurfaceWidth - ScaleX(18);
    WordWrap := True;
    if OllamaUninstaller <> '' then
      Caption := 'Your models and your ollama.com sign-in are kept. Ollama''s app settings and chats are copied to %USERPROFILE%\.ollama before its uninstaller removes them.'
    else
      Caption := 'This Ollama has no uninstaller entry; remove it by hand if you want to.';
    OllamaKeepRadio := TNewRadioButton.Create(OllamaPage);
    OllamaKeepRadio.Parent := OllamaPage.Surface;
    OllamaKeepRadio.Top := Top + Height + ScaleY(12);
  end;
  OllamaKeepRadio.Width := OllamaPage.SurfaceWidth;
  OllamaKeepRadio.Caption := 'Keep Ollama and run xOllama beside it';
  OllamaKeepRadio.Checked := OllamaUninstaller = '';

  with TNewStaticText.Create(OllamaPage) do begin
    Parent := OllamaPage.Surface;
    Left := ScaleX(18);
    Top := OllamaKeepRadio.Top + OllamaKeepRadio.Height + ScaleY(2);
    Width := OllamaPage.SurfaceWidth - ScaleX(18);
    WordWrap := True;
    Caption := 'Ollama keeps port ' + OllamaDefaultPort + ', so xOllama takes another. Models loaded in both at once share the same GPU memory.';
  end;
end;

// ------------------------------------------------------------------ port

// XOLLAMA_HOST as the user has it, split so a new port keeps their scheme and
// host: [scheme://]host[:port].
procedure ParseExistingHost();
var
  Rest: string;
  P, I: Integer;
begin
  HostScheme := '';
  HostName := '127.0.0.1';
  ChosenPort := '';
  if not RegQueryStringValue(HKEY_CURRENT_USER, 'Environment', 'XOLLAMA_HOST', ExistingHost) then
    ExistingHost := '';
  Rest := Trim(ExistingHost);
  if Rest = '' then
    exit;
  P := Pos('://', Rest);
  if P > 0 then begin
    HostScheme := Copy(Rest, 1, P + 2);
    Rest := Copy(Rest, P + 3, Length(Rest));
  end;
  P := 0;
  for I := Length(Rest) downto 1 do
    if Rest[I] = ':' then begin
      P := I;
      break;
    end;
  if P > 0 then begin
    if P > 1 then
      HostName := Copy(Rest, 1, P - 1);
    ChosenPort := Copy(Rest, P + 1, Length(Rest));
  end else if Rest <> '' then
    HostName := Rest;
end;

procedure PortRadioClick(Sender: TObject);
begin
  PortCustomEdit.Enabled := PortCustomRadio.Checked;
end;

procedure CreatePortPage(AfterID: Integer);
var
  Text: TNewStaticText;
  Current: string;
begin
  ParseExistingHost();
  Current := ChosenPort;
  if Current = '' then
    Current := XollamaDefaultPort;

  PortPage := CreateCustomPage(AfterID, 'Port',
    'Choose the port xOllama listens on.');

  Text := TNewStaticText.Create(PortPage);
  Text.Parent := PortPage.Surface;
  Text.WordWrap := True;
  Text.Width := PortPage.SurfaceWidth;
  Text.Caption := 'Apps and the xollama command line connect here. Other computers reach it only after you turn on "Expose" in the xOllama settings.';

  PortXollamaRadio := TNewRadioButton.Create(PortPage);
  PortXollamaRadio.Parent := PortPage.Surface;
  PortXollamaRadio.Top := Text.Top + Text.Height + ScaleY(16);
  PortXollamaRadio.Width := PortPage.SurfaceWidth;
  PortXollamaRadio.Caption := XollamaDefaultPort + ' - the xOllama default';
  PortXollamaRadio.OnClick := @PortRadioClick;

  PortOllamaRadio := TNewRadioButton.Create(PortPage);
  PortOllamaRadio.Parent := PortPage.Surface;
  PortOllamaRadio.Top := PortXollamaRadio.Top + PortXollamaRadio.Height + ScaleY(8);
  PortOllamaRadio.Width := PortPage.SurfaceWidth;
  PortOllamaRadio.Caption := OllamaDefaultPort + ' - the Ollama default: apps set up for Ollama work unchanged';
  PortOllamaRadio.OnClick := @PortRadioClick;

  PortCustomRadio := TNewRadioButton.Create(PortPage);
  PortCustomRadio.Parent := PortPage.Surface;
  PortCustomRadio.Top := PortOllamaRadio.Top + PortOllamaRadio.Height + ScaleY(8);
  PortCustomRadio.Width := ScaleX(90);
  PortCustomRadio.Caption := 'Custom:';
  PortCustomRadio.OnClick := @PortRadioClick;

  PortCustomEdit := TNewEdit.Create(PortPage);
  PortCustomEdit.Parent := PortPage.Surface;
  PortCustomEdit.Left := PortCustomRadio.Left + PortCustomRadio.Width + ScaleX(4);
  PortCustomEdit.Top := PortCustomRadio.Top - ScaleY(2);
  PortCustomEdit.Width := ScaleX(80);

  if Current = XollamaDefaultPort then
    PortXollamaRadio.Checked := True
  else if Current = OllamaDefaultPort then
    PortOllamaRadio.Checked := True
  else begin
    PortCustomRadio.Checked := True;
    PortCustomEdit.Text := Current;
  end;
  PortRadioClick(nil);
end;

// 11434 is Ollama's while an Ollama stays installed. Re-evaluated each time
// the page is shown, since the page before it may have just removed Ollama.
procedure RefreshPortPage();
begin
  PortOllamaRadio.Enabled := not OllamaFound;
  if OllamaFound then begin
    PortOllamaRadio.Caption := OllamaDefaultPort + ' - in use by Ollama';
    if PortOllamaRadio.Checked then
      PortXollamaRadio.Checked := True;
  end else
    PortOllamaRadio.Caption := OllamaDefaultPort + ' - the Ollama default: apps set up for Ollama work unchanged';
  PortRadioClick(nil);
end;

function PortPageValid(): Boolean;
var
  N: Integer;
begin
  Result := True;
  if PortXollamaRadio.Checked then
    ChosenPort := XollamaDefaultPort
  else if PortOllamaRadio.Checked then
    ChosenPort := OllamaDefaultPort
  else begin
    N := StrToIntDef(Trim(PortCustomEdit.Text), 0);
    if (N < 1) or (N > 65535) then begin
      MsgBox('Enter a port between 1 and 65535.', mbError, MB_OK);
      Result := False;
      exit;
    end;
    if OllamaFound and (IntToStr(N) = OllamaDefaultPort) then begin
      MsgBox('Port ' + OllamaDefaultPort + ' is in use by Ollama. Choose another.', mbError, MB_OK);
      Result := False;
      exit;
    end;
    ChosenPort := IntToStr(N);
  end;
end;

// The XOLLAMA_HOST this install writes, or '' to leave it alone: the default
// port needs no variable unless the user already had one.
function NewHostValue(): string;
begin
  Result := '';
  if ChosenPort = '' then
    exit;
  if (ExistingHost = '') and (ChosenPort = XollamaDefaultPort) then
    exit;
  Result := HostScheme + HostName + ':' + ChosenPort;
end;

// [Registry] writes a new variable with uninsdeletevalue; one the user had
// before is rewritten from code, so uninstalling does not remove their own.
function HostIsNew(): Boolean;
begin
  Result := (ExistingHost = '') and (NewHostValue() <> '');
end;

function HostValue(Param: string): string;
begin
  Result := NewHostValue();
end;

// --------------------------------------------------------------- API key

function BCryptGenRandom(hAlgorithm: Longint; pbBuffer: AnsiString; cbBuffer: Longint; dwFlags: Longint): Longint;
  external 'BCryptGenRandom@bcrypt.dll stdcall';

const
  BCRYPT_USE_SYSTEM_PREFERRED_RNG = 2;
  Base64URL = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';

// Unpadded base64url, as the server's own generator (newAPIKey) writes it.
function EncodeBase64URL(Data: AnsiString): string;
var
  I, N, B: Integer;
begin
  Result := '';
  N := Length(Data);
  I := 1;
  while I <= N do begin
    B := Ord(Data[I]) shl 16;
    if I + 1 <= N then B := B or (Ord(Data[I + 1]) shl 8);
    if I + 2 <= N then B := B or Ord(Data[I + 2]);
    // One character per statement: Char + Char is not a string in Pascal Script.
    Result := Result + Copy(Base64URL, ((B shr 18) and 63) + 1, 1);
    Result := Result + Copy(Base64URL, ((B shr 12) and 63) + 1, 1);
    if I + 1 <= N then Result := Result + Copy(Base64URL, ((B shr 6) and 63) + 1, 1);
    if I + 2 <= N then Result := Result + Copy(Base64URL, (B and 63) + 1, 1);
    I := I + 3;
  end;
end;

// 256 random bits from the system RNG, in the server's own key format.
function NewAPIKey(): string;
var
  Buf: AnsiString;
begin
  Result := '';
  SetLength(Buf, 32);
  if BCryptGenRandom(0, Buf, 32, BCRYPT_USE_SYSTEM_PREFERRED_RNG) <> 0 then
    exit;
  Result := 'xok_' + EncodeBase64URL(Buf);
end;

procedure GenerateKeyClick(Sender: TObject);
var
  K: string;
begin
  K := NewAPIKey();
  if K = '' then
    MsgBox('Could not get random bytes from Windows. Paste a key of your own instead.', mbError, MB_OK)
  else
    KeyEdit.Text := K;
end;

// clip.exe from a file, so no character of the key ever meets a command line.
procedure CopyKeyClick(Sender: TObject);
var
  Tmp: string;
  RC: Integer;
begin
  if Trim(KeyEdit.Text) = '' then
    exit;
  Tmp := ExpandConstant('{tmp}\xollama-key.txt');
  if SaveStringToFile(Tmp, Trim(KeyEdit.Text), False) then begin
    Exec(ExpandConstant('{cmd}'), '/C clip < "' + Tmp + '"', '', SW_HIDE, ewWaitUntilTerminated, RC);
    DeleteFile(Tmp);
  end;
end;

procedure CreateKeyPage(AfterID: Integer);
var
  Text: TNewStaticText;
  GenButton, CopyButton: TNewButton;
begin
  KeyExisting := FileExists(ExpandConstant('{%USERPROFILE}\.ollama\xollama-server.json'));
  KeyPage := CreateCustomPage(AfterID, 'API key',
    'Optionally require a key from every client. You can skip this step.');

  Text := TNewStaticText.Create(KeyPage);
  Text.Parent := KeyPage.Surface;
  Text.WordWrap := True;
  Text.Width := KeyPage.SurfaceWidth;
  if KeyExisting then
    Text.Caption := 'A key is already set. Leave the box empty to keep it, or enter a new one to replace it.'
  else
    Text.Caption := 'Leave the box empty to skip: xOllama then answers any client that can reach it.' + #13#10#13#10 +
      'With a key, clients send it as "Authorization: Bearer <key>". The xollama CLI and this app use it without being told; other apps need it pasted in.';

  KeyEdit := TNewEdit.Create(KeyPage);
  KeyEdit.Parent := KeyPage.Surface;
  KeyEdit.Top := Text.Top + Text.Height + ScaleY(16);
  KeyEdit.Width := KeyPage.SurfaceWidth;

  GenButton := TNewButton.Create(KeyPage);
  GenButton.Parent := KeyPage.Surface;
  GenButton.Top := KeyEdit.Top + KeyEdit.Height + ScaleY(8);
  GenButton.Width := ScaleX(110);
  GenButton.Height := WizardForm.NextButton.Height;
  GenButton.Caption := 'Generate';
  GenButton.OnClick := @GenerateKeyClick;

  CopyButton := TNewButton.Create(KeyPage);
  CopyButton.Parent := KeyPage.Surface;
  CopyButton.Top := GenButton.Top;
  CopyButton.Left := GenButton.Left + GenButton.Width + ScaleX(8);
  CopyButton.Width := ScaleX(160);
  CopyButton.Height := WizardForm.NextButton.Height;
  CopyButton.Caption := 'Copy to clipboard';
  CopyButton.OnClick := @CopyKeyClick;

  with TNewStaticText.Create(KeyPage) do begin
    Parent := KeyPage.Surface;
    Top := GenButton.Top + GenButton.Height + ScaleY(12);
    Width := KeyPage.SurfaceWidth;
    WordWrap := True;
    Caption := 'Only a digest of the key is stored for the server; your own copy goes to %USERPROFILE%\.ollama\xollama-api-key. Over a network, send it only through TLS.';
  end;
end;

function KeyPageValid(): Boolean;
begin
  Result := True;
  ChosenKey := Trim(KeyEdit.Text);
  if (ChosenKey <> '') and (Length(ChosenKey) < MinAPIKeyLength) then begin
    MsgBox(Format('A key must be at least %d characters. Generate one, or leave the box empty to skip.', [MinAPIKeyLength]), mbError, MB_OK);
    Result := False;
  end;
end;

// The server's ~/.ollama/xollama-server.json and this user's client copy, in
// the formats envconfig/xollama_apikey.go reads.
procedure WriteAPIKey();
var
  Dir: string;
begin
  if ChosenKey = '' then
    exit;
  Dir := ExpandConstant('{%USERPROFILE}\.ollama');
  ForceDirectories(Dir);
  if not SaveStringToFile(Dir + '\xollama-server.json',
       '{' + #10 + '  "api_key_sha256": "' + GetSHA256OfString(ChosenKey) + '"' + #10 + '}' + #10, False) then
    MsgBox('Could not write ' + Dir + '\xollama-server.json; the API key was not set.', mbError, MB_OK)
  else if not SaveStringToFile(Dir + '\xollama-api-key', ChosenKey + #10, False) then
    MsgBox('The server has the key, but this user''s copy could not be written to ' + Dir + '\xollama-api-key.', mbError, MB_OK)
  else
    Log('API key set');
end;

// -------------------------------------------------------------- KV cache

var
  KVPage: TWizardPage;
  KCombo, VCombo, LegacyCombo: TNewComboBox;
  KExisting, VExisting, LegacyExisting: string;
  KChosen, VChosen, LegacyChosen: string;   // '' = leave the variable alone

const
  LeaveAsIs = '(leave as is)';
  // opencoti's own list (common/arg.cpp kv_cache_types + kvarn widths),
  // without the frozen turbo tiers.
  OpencotiKVTypes = 'f16,bf16,f32,q8_0,q6_0,q5_1,q5_0,q4_1,q4_0,iq4_nl,kvarn8,kvarn6,kvarn5,kvarn4,kvarn3,kvarn2';
  // Types an upstream ollama/llama.cpp load takes.
  LegacyKVTypes = 'f16,q8_0,q4_0';

function EnvValue(Name: string): string;
begin
  if not RegQueryStringValue(HKEY_CURRENT_USER, 'Environment', Name, Result) then
    Result := '';
  Result := Trim(Result);
end;

procedure FillCombo(Combo: TNewComboBox; Types, Existing: string);
var
  Rest, T: string;
  P: Integer;
begin
  if Existing <> '' then
    Combo.Items.Add(LeaveAsIs + ': ' + Existing)
  else
    Combo.Items.Add(LeaveAsIs + ': not set');
  Rest := Types;
  while Rest <> '' do begin
    P := Pos(',', Rest);
    if P = 0 then begin
      T := Rest;
      Rest := '';
    end else begin
      T := Copy(Rest, 1, P - 1);
      Rest := Copy(Rest, P + 1, Length(Rest));
    end;
    Combo.Items.Add(T);
  end;
  Combo.ItemIndex := 0;
end;

function ComboChoice(Combo: TNewComboBox): string;
begin
  Result := '';
  if Combo.ItemIndex > 0 then
    Result := Combo.Items[Combo.ItemIndex];
end;

function NewLabel(Page: TWizardPage; Top: Integer; Caption: string): TNewStaticText;
begin
  Result := TNewStaticText.Create(Page);
  Result.Parent := Page.Surface;
  Result.Top := Top;
  Result.Width := Page.SurfaceWidth;
  Result.WordWrap := True;
  Result.Caption := Caption;
end;

function NewCombo(Page: TWizardPage; Left, Top, Width: Integer): TNewComboBox;
begin
  Result := TNewComboBox.Create(Page);
  Result.Parent := Page.Surface;
  Result.Style := csDropDownList;
  Result.Left := Left;
  Result.Top := Top;
  Result.Width := Width;
end;

procedure CreateKVPage(AfterID: Integer);
var
  L: TNewStaticText;
  Y, W: Integer;
begin
  KExisting := EnvValue('XOLLAMA_K_CACHE_TYPE');
  VExisting := EnvValue('XOLLAMA_V_CACHE_TYPE');
  LegacyExisting := EnvValue('XOLLAMA_KV_CACHE_TYPE');
  KVPage := CreateCustomPage(AfterID, 'KV cache',
    'Server-wide cache types. A model''s own settings (xollama tweak model) still win.');
  W := (KVPage.SurfaceWidth - ScaleX(16)) div 2;

  L := NewLabel(KVPage, 0, 'opencoti engine - keys and values separately. Keys lose more quality to compression, so a wider K than V is the usual recipe. KVarN keys need KVarN values (any pair of widths).');
  Y := L.Top + L.Height + ScaleY(8);
  NewLabel(KVPage, Y, 'Keys (K)');
  with NewLabel(KVPage, Y, 'Values (V)') do Left := W + ScaleX(16);
  Y := Y + ScaleY(18);
  KCombo := NewCombo(KVPage, 0, Y, W);
  VCombo := NewCombo(KVPage, W + ScaleX(16), Y, W);
  FillCombo(KCombo, OpencotiKVTypes, KExisting);
  FillCombo(VCombo, OpencotiKVTypes, VExisting);

  L := NewLabel(KVPage, KCombo.Top + KCombo.Height + ScaleY(18),
    'Legacy llama.cpp - one type for both halves, used when stock llama.cpp serves a load (a device opencoti does not cover). Only xOllama reads it; an Ollama beside it keeps its own OLLAMA_KV_CACHE_TYPE.');
  LegacyCombo := NewCombo(KVPage, 0, L.Top + L.Height + ScaleY(6), W);
  FillCombo(LegacyCombo, LegacyKVTypes, LegacyExisting);
  if (LegacyExisting = '') and (GetEnv('OLLAMA_KV_CACHE_TYPE') <> '') then
    LegacyCombo.Items[0] := LeaveAsIs + ': OLLAMA_KV_CACHE_TYPE=' + GetEnv('OLLAMA_KV_CACHE_TYPE');
end;

function IsKVarN(T: string): Boolean;
begin
  Result := Pos('kvarn', T) = 1;
end;

function KVPageValid(): Boolean;
var
  K, V: string;
begin
  Result := True;
  KChosen := ComboChoice(KCombo);
  VChosen := ComboChoice(VCombo);
  LegacyChosen := ComboChoice(LegacyCombo);
  // What the engine will actually see: a half left alone keeps its current value.
  K := KChosen;
  if K = '' then K := KExisting;
  V := VChosen;
  if V = '' then V := VExisting;
  if (K <> '') and (V <> '') and (IsKVarN(K) <> IsKVarN(V)) then begin
    MsgBox('K = ' + K + ' and V = ' + V + ': a KVarN width needs a KVarN width on the other half too (the engine would force it). Pick kvarnN for both, or plain types for both.', mbError, MB_OK);
    Result := False;
  end;
end;

// Written by [Registry] (with uninsdeletevalue) when the variable is new, by
// code when the user already had one, as XOLLAMA_HOST is.
function KVValue(Param: string): string;
begin
  if Param = 'K' then Result := KChosen
  else if Param = 'V' then Result := VChosen
  else Result := LegacyChosen;
end;

function KVExisting(Param: string): string;
begin
  if Param = 'K' then Result := KExisting
  else if Param = 'V' then Result := VExisting
  else Result := LegacyExisting;
end;

function KVIsNew(Param: string): Boolean;
begin
  Result := (KVValue(Param) <> '') and (KVExisting(Param) = '');
end;

procedure WriteChangedKV();
begin
  if (KChosen <> '') and (KExisting <> '') then
    RegWriteExpandStringValue(HKEY_CURRENT_USER, 'Environment', 'XOLLAMA_K_CACHE_TYPE', KChosen);
  if (VChosen <> '') and (VExisting <> '') then
    RegWriteExpandStringValue(HKEY_CURRENT_USER, 'Environment', 'XOLLAMA_V_CACHE_TYPE', VChosen);
  if (LegacyChosen <> '') and (LegacyExisting <> '') then
    RegWriteExpandStringValue(HKEY_CURRENT_USER, 'Environment', 'XOLLAMA_KV_CACHE_TYPE', LegacyChosen);
end;

// ---------------------------------------------------------------- wiring

procedure InitializeWizard();
var
  After: Integer;
begin
  DetectOllama();
  After := wpWelcome;
  if OllamaFound then begin
    CreateOllamaPage(After);
    After := OllamaPage.ID;
  end;
  CreatePortPage(After);
  CreateKeyPage(PortPage.ID);
  CreateKVPage(KeyPage.ID);
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if CurPageID = PortPage.ID then
    RefreshPortPage();
end;

function NextButtonClick(CurPageID: Integer): Boolean;
begin
  Result := True;
  if (OllamaPage <> nil) and (CurPageID = OllamaPage.ID) then begin
    if OllamaRemoveRadio.Checked and not OllamaRemoved then begin
      Result := UninstallOllama();
      OllamaRemoved := Result;
    end;
  end else if CurPageID = PortPage.ID then
    Result := PortPageValid()
  else if CurPageID = KeyPage.ID then
    Result := KeyPageValid()
  else if CurPageID = KVPage.ID then
    Result := KVPageValid();
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  V: string;
begin
  if CurStep <> ssPostInstall then
    exit;
  V := NewHostValue();
  if (V <> '') and not HostIsNew() then
    RegWriteExpandStringValue(HKEY_CURRENT_USER, 'Environment', 'XOLLAMA_HOST', V);
  WriteChangedKV();
  WriteAPIKey();
end;

// The first launch comes from this installer, whose environment predates the
// variables it just wrote; hand them over explicitly.
function AppRunParams(Param: string): string;
var
  V: string;
begin
  Result := '/C set PATH=' + ExpandConstant('{app}') + ';%PATH% & ';
  V := NewHostValue();
  if V <> '' then
    Result := Result + 'set "XOLLAMA_HOST=' + V + '" & ';
  if KChosen <> '' then
    Result := Result + 'set "XOLLAMA_K_CACHE_TYPE=' + KChosen + '" & ';
  if VChosen <> '' then
    Result := Result + 'set "XOLLAMA_V_CACHE_TYPE=' + VChosen + '" & ';
  if LegacyChosen <> '' then
    Result := Result + 'set "XOLLAMA_KV_CACHE_TYPE=' + LegacyChosen + '" & ';
  Result := Result + '"' + ExpandConstant('{app}\{#MyAppExeName}') + '"';
end;
