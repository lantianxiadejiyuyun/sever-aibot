param([string]$Exe = (Join-Path $PSScriptRoot '../../dist/windows-client/AIBOT-Client.exe'), [string]$Artifacts = "$env:TEMP\orca-client-ui-check")
$ErrorActionPreference = "Stop"
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes, System.Drawing
Add-Type @'
using System;
using System.Runtime.InteropServices;
public class NativeUI {
 [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr h, uint m, IntPtr w, IntPtr l);
 [DllImport("user32.dll")] public static extern IntPtr SendMessage(IntPtr h, uint m, IntPtr w, IntPtr l);
 [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
 [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
 public struct RECT {public int L,T,R,B;}
}
'@
New-Item -ItemType Directory -Force $Artifacts | Out-Null
$oldLocal = $env:LOCALAPPDATA
$env:LOCALAPPDATA = Join-Path $Artifacts ([guid]::NewGuid().ToString())
New-Item -ItemType Directory -Force $env:LOCALAPPDATA | Out-Null
$p = $null
$runPath = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$runItem = Get-ItemProperty -Path $runPath -Name AIBOTClient -ErrorAction SilentlyContinue
$originalRun = if($runItem) { $runItem.AIBOTClient } else { $null }
function Wait-Window([int]$ProcessId) {
 for($i=0;$i -lt 60;$i++) {
  $all = [Windows.Automation.AutomationElement]::RootElement.FindAll([Windows.Automation.TreeScope]::Children,[Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::ProcessIdProperty,$ProcessId))
  foreach($a in $all) { if($a.Current.Name -eq 'AIBOT 设备客户端') {return $a} }
  Start-Sleep -Milliseconds 200
 }
 throw "Main window did not appear (process $ProcessId; windows: $(@($all | ForEach-Object {$_.Current.Name}) -join ', '))"
}
function Button($window,[string]$text) {
 $b=$window.FindFirst([Windows.Automation.TreeScope]::Descendants,[Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::NameProperty,$text))
 if(!$b){throw "Missing button $text"}
 $b.GetCurrentPattern([Windows.Automation.InvokePattern]::Pattern).Invoke()
 Start-Sleep -Milliseconds 200
}
function Key-Masked($window) {
 $current=$window.FindAll([Windows.Automation.TreeScope]::Descendants,[Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::ControlTypeProperty,[Windows.Automation.ControlType]::Edit))
 return $current[2].Current.IsPassword
}
function Shot($window,[string]$name) {
 $r=$window.Current.BoundingRectangle
 $b=New-Object Drawing.Bitmap ([int]$r.Width),([int]$r.Height)
 $g=[Drawing.Graphics]::FromImage($b);$g.CopyFromScreen([int]$r.X,[int]$r.Y,0,0,$b.Size)
 $b.Save((Join-Path $Artifacts $name));$g.Dispose();$b.Dispose()
}
try {
 $p=Start-Process $Exe -PassThru
 $w=Wait-Window $p.Id
 Start-Sleep -Milliseconds 1200
 $edits=$w.FindAll([Windows.Automation.TreeScope]::Descendants,[Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::ControlTypeProperty,[Windows.Automation.ControlType]::Edit))
 $edits | ForEach-Object { "Edit: id=$($_.Current.AutomationId), password=$($_.Current.IsPassword)" }
 if($edits.Count -lt 5 -or !(Key-Masked $w)){throw 'Password control missing or unmasked'}
 $url=$edits[0].GetCurrentPattern([Windows.Automation.ValuePattern]::Pattern)
 if($url.Current.Value -ne 'https://aibot.eugenweb.cn'){throw 'Wrong default URL'}
 # Saving invalid input exercises local validation without any network connection.
 $url.SetValue('invalid-url')
 Button $w '仅保存配置'
 Start-Sleep -Milliseconds 700
 Shot $w 'validation.png'
 $dialogs=$w.FindAll([Windows.Automation.TreeScope]::Descendants,[Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::ControlTypeProperty,[Windows.Automation.ControlType]::Window))
 if($dialogs.Count -eq 0){throw 'Expected visible validation dialog'}
 [NativeUI]::PostMessage([IntPtr]$dialogs[0].Current.NativeWindowHandle,0x10,[IntPtr]::Zero,[IntPtr]::Zero)|Out-Null
 Start-Sleep -Milliseconds 200
 $url.SetValue('http://127.0.0.1:1')
 $edits[1].GetCurrentPattern([Windows.Automation.ValuePattern]::Pattern).SetValue('ui-test-device')
 $edits[2].GetCurrentPattern([Windows.Automation.ValuePattern]::Pattern).SetValue('ui-test-secret-not-real')
 Button $w '仅保存配置'; Start-Sleep -Milliseconds 500
 $dialogs=$w.FindAll([Windows.Automation.TreeScope]::Descendants,[Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::ControlTypeProperty,[Windows.Automation.ControlType]::Window))
 foreach($d in $dialogs){[NativeUI]::PostMessage([IntPtr]$d.Current.NativeWindowHandle,0x10,[IntPtr]::Zero,[IntPtr]::Zero)|Out-Null}
 Start-Sleep -Milliseconds 250
 # Save alone must not start the Agent; this reproduces the reported disconnected/empty-log state.
 $savedLog=$w.FindFirst([Windows.Automation.TreeScope]::Descendants,[Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::ControlTypeProperty,[Windows.Automation.ControlType]::Document))
 if(![string]::IsNullOrWhiteSpace($savedLog.GetCurrentPattern([Windows.Automation.TextPattern]::Pattern).DocumentRange.GetText(-1))){throw 'Save-only unexpectedly produced connection logs'}
 $disconnect=$w.FindFirst([Windows.Automation.TreeScope]::Descendants,[Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::NameProperty,'断开'))
 if($disconnect.Current.IsEnabled){throw 'Save-only unexpectedly connected'}
 Shot $w 'saved-not-connected.png'
 Button $w '显示密钥'
 Shot $w 'revealed-test-key.png'
 $edits=$w.FindAll([Windows.Automation.TreeScope]::Descendants,[Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::ControlTypeProperty,[Windows.Automation.ControlType]::Edit))
 if((Key-Masked $w)){throw 'Explicit reveal did not show key'}
 if($edits[2].GetCurrentPattern([Windows.Automation.ValuePattern]::Pattern).Current.Value -ne 'ui-test-secret-not-real'){throw 'Wrong revealed saved key'}
 Button $w '隐藏密钥'
 if(!(Key-Masked $w)){throw 'Hide did not mask key'}
 # Change the form without saving first: the primary action must persist these edits.
 $edits[1].GetCurrentPattern([Windows.Automation.ValuePattern]::Pattern).SetValue('ui-test-device-edited')
 $edits[2].GetCurrentPattern([Windows.Automation.ValuePattern]::Pattern).SetValue('ui-test-secret-edited-not-real')
 Button $w '显示密钥'
 Button $w '保存并连接'; Start-Sleep -Milliseconds 1500
 if(!(Key-Masked $w)){throw 'Connect did not remask key'}
 if(!$disconnect.Current.IsEnabled){throw 'Save-and-connect did not start Agent'}
 if(!$url.Current.IsReadOnly){throw 'Config remained editable while connecting'}
 $saved=Get-Content (Join-Path $env:LOCALAPPDATA 'AIBOT/Client/config.json') -Raw | ConvertFrom-Json
 if($saved.deviceId -ne 'ui-test-device-edited'){throw 'Connect ignored unsaved edits'}
 Shot $w 'connected-local-error.png'
 $logControl=$w.FindFirst([Windows.Automation.TreeScope]::Descendants,[Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::ControlTypeProperty,[Windows.Automation.ControlType]::Document))
 $text=$logControl.GetCurrentPattern([Windows.Automation.TextPattern]::Pattern).DocumentRange.GetText(-1)
 if([string]::IsNullOrWhiteSpace($text)){throw 'Connection attempt did not produce logs'}
 if($text.Contains('ui-test-secret-not-real') -or $text.Contains('ui-test-secret-edited-not-real')){throw 'Secret leaked in GUI logs'}
 Button $w '断开'; Start-Sleep -Milliseconds 700
 if($url.Current.IsReadOnly){throw 'Config remained locked after disconnect'}
 Shot $w 'main-window.png'
 Button $w '显示密钥'
 $handle=[IntPtr]$w.Current.NativeWindowHandle
 [NativeUI]::PostMessage($handle,0x10,[IntPtr]::Zero,[IntPtr]::Zero)|Out-Null
 Start-Sleep -Milliseconds 400
 $p.Refresh();if($p.HasExited){throw 'Close unexpectedly exited'}
 $second=Start-Process $Exe -PassThru
 if(!$second.WaitForExit(8000)){throw 'Second instance remained alive'}
 $w=Wait-Window $p.Id
 if(!(Key-Masked $w)){throw 'Tray restore exposed key'}
 Shot $w 'restored-window.png'
 'PASS: defaults, masking/reveal/hide, validation, save-only, save-and-connect applies edits, config locking, local connect/disconnect, log redaction, tray remasking, second-instance restore'
 # Invoke Exit through the actual tray context menu using its standard callback.
 [NativeUI]::PostMessage($handle,0x8000,[IntPtr]::Zero,[IntPtr]0x205)|Out-Null
 Start-Sleep -Milliseconds 300
 $exit=[Windows.Automation.AutomationElement]::RootElement.FindFirst([Windows.Automation.TreeScope]::Descendants,[Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::NameProperty,'退出'))
 if(!$exit){throw 'Tray Exit menu unavailable'}
 $exit.GetCurrentPattern([Windows.Automation.InvokePattern]::Pattern).Invoke()
 if(!$p.WaitForExit(8000)){throw 'Tray exit did not terminate process'}
 'PASS: tray Exit stops process'
 $p=Start-Process $Exe -ArgumentList '--background' -PassThru
 Start-Sleep -Milliseconds 1500
 $p.Refresh(); if($p.HasExited -or $p.MainWindowHandle -ne 0){throw 'Background startup failed'}
 $second=Start-Process $Exe -PassThru; $second.WaitForExit(8000)|Out-Null
 $w=Wait-Window $p.Id
 $edits=$w.FindAll([Windows.Automation.TreeScope]::Descendants,[Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::ControlTypeProperty,[Windows.Automation.ControlType]::Edit))
 if(!(Key-Masked $w)){throw 'Reloaded saved key was not masked'}
 Button $w '显示密钥'
 $edits=$w.FindAll([Windows.Automation.TreeScope]::Descendants,[Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::ControlTypeProperty,[Windows.Automation.ControlType]::Edit))
 if($edits[2].GetCurrentPattern([Windows.Automation.ValuePattern]::Pattern).Current.Value -ne 'ui-test-secret-edited-not-real'){throw 'Reloaded saved key differs'}
 Button $w '隐藏密钥'
 'PASS: --background stays hidden; second launch restores; persisted key loads masked and reveals only on click'
 [NativeUI]::PostMessage([IntPtr]$w.Current.NativeWindowHandle,0x8000,[IntPtr]::Zero,[IntPtr]0x205)|Out-Null
 Start-Sleep -Milliseconds 300
 $exit=[Windows.Automation.AutomationElement]::RootElement.FindFirst([Windows.Automation.TreeScope]::Descendants,[Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::NameProperty,'退出'))
 $exit.GetCurrentPattern([Windows.Automation.InvokePattern]::Pattern).Invoke()
 if(!$p.WaitForExit(8000)){throw 'Background process did not exit'}
 "Artifacts: $Artifacts"
} finally {
 if($p -and !$p.HasExited){Stop-Process -Id $p.Id -ErrorAction SilentlyContinue}
 $env:LOCALAPPDATA=$oldLocal
 if($null -ne $originalRun){Set-ItemProperty -Path $runPath -Name AIBOTClient -Value $originalRun} else {Remove-ItemProperty -Path $runPath -Name AIBOTClient -ErrorAction SilentlyContinue}
}
