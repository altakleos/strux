# Installs strux on Windows, at the release this script is published with. In PowerShell:
#
#   irm https://github.com/altakleos/strux/releases/latest/download/install.ps1 | iex
#
# It downloads the release's archive for this machine and checks its SHA-256 against the
# release's checksums.txt. When gh is installed and logged in to github.com, it also checks the
# archive's build provenance: that altakleos/strux's release workflow built it from this
# release's tag. It installs strux.exe into ~\.local\bin, replacing any strux.exe there. It runs
# in Windows PowerShell 5.1 and in PowerShell 7.
#
# $env:STRUX_INSTALL_DIR names another directory to install into. $env:STRUX_RELEASE_URL names
# another place to download the release's assets from, such as a mirror; the provenance check
# still requires them to be this release's.
#
# Everything runs in one script block, so a partial download runs nothing, and nothing the
# script sets outlasts it.

& {
	$ErrorActionPreference = 'Stop'
	$ProgressPreference = 'SilentlyContinue' # Invoke-WebRequest's progress bar slows downloads.

	$version = '@VERSION@'
	$repo = 'altakleos/strux'

	# Runs a native command, and returns its exit code and its output, stderr included. Windows
	# PowerShell turns a native command's redirected stderr into errors, which Stop makes fatal.
	function Invoke-Native($command) {
		$ErrorActionPreference = 'Continue'
		$output = & $command @args 2>&1 | ForEach-Object { "$_" }
		[pscustomobject]@{ ExitCode = $LASTEXITCODE; Output = $output -join "`n" }
	}

	# A 32-bit PowerShell on a 64-bit machine names the machine's architecture in
	# PROCESSOR_ARCHITEW6432.
	$machine = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
	$arch = switch ($machine) {
		'AMD64' { 'amd64' }
		'ARM64' { 'arm64' }
		default { throw "strux has no release for $machine machines" }
	}
	$release = if ($env:STRUX_RELEASE_URL) { $env:STRUX_RELEASE_URL } else { "https://github.com/$repo/releases/download/v$version" }
	$dir = if ($env:STRUX_INSTALL_DIR) { $env:STRUX_INSTALL_DIR } else { Join-Path $HOME '.local\bin' }
	$archive = "strux_${version}_windows_$arch.zip"
	$exe = Join-Path $dir 'strux.exe'
	$staged = Join-Path $dir ".strux.$PID.exe"
	$tmp = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
	New-Item -ItemType Directory -Path $tmp | Out-Null
	try {
		Write-Host "Installing strux $version for windows/$arch from $release"
		foreach ($name in 'checksums.txt', $archive) {
			try {
				Invoke-WebRequest -UseBasicParsing -Uri "$release/$name" -OutFile (Join-Path $tmp $name)
			} catch {
				throw "could not download $release/${name}: $_"
			}
		}
		$zip = Join-Path $tmp $archive

		$want = foreach ($line in Get-Content -LiteralPath (Join-Path $tmp 'checksums.txt')) {
			$sum, $name = $line -split '  ', 2
			if ($name -eq $archive) { $sum }
		}
		if (-not $want) { throw "checksums.txt lists no $archive" }
		$got = (Get-FileHash -Algorithm SHA256 -LiteralPath $zip).Hash.ToLowerInvariant()
		if ($got -ne $want) { throw "$archive has SHA-256 $got, but checksums.txt lists $want" }
		Write-Host '  checksum:   matches checksums.txt'

		# gh checks that the release workflow built the archive from this release's tag. Without a
		# gh that can, the script says so and goes on.
		if (-not (Get-Command gh -CommandType Application -ErrorAction SilentlyContinue)) {
			Write-Host '  provenance: not checked, since gh is not installed'
		} elseif ((Invoke-Native gh attestation verify --help).Output -notmatch '--source-ref') {
			Write-Host '  provenance: not checked, since this gh is too old'
		} elseif ((Invoke-Native gh auth status --hostname github.com).ExitCode -ne 0) {
			Write-Host '  provenance: not checked, since gh is not logged in to github.com'
		} else {
			$verify = Invoke-Native gh attestation verify $zip --repo $repo `
				--signer-workflow "$repo/.github/workflows/release.yml" `
				--source-ref "refs/tags/v$version" --deny-self-hosted-runners
			if ($verify.ExitCode -ne 0) {
				throw "gh could not verify that $repo's release workflow built $archive from v${version}:`n$($verify.Output)"
			}
			Write-Host "  provenance: verified by gh: built by $repo's release workflow from v$version"
		}

		Expand-Archive -LiteralPath $zip -DestinationPath (Join-Path $tmp 'archive')

		# The new binary runs before it replaces the old one, and replaces it by a rename, which
		# leaves no partly written strux.exe.
		New-Item -ItemType Directory -Force -Path $dir | Out-Null
		Copy-Item -LiteralPath (Join-Path $tmp 'archive\strux.exe') -Destination $staged
		$run = Invoke-Native $staged version
		if ($run.ExitCode -ne 0) { throw "strux $version does not run on this machine:`n$($run.Output)" }
		Move-Item -Force -LiteralPath $staged -Destination $exe
		Write-Host "  installed:  $exe"

		# Says when running strux by name would not run the strux just installed.
		$found = Get-Command strux -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
		if ($found.Source -ne $exe) {
			if (($env:Path -split ';' | ForEach-Object { $_.TrimEnd('\') }) -contains $dir.TrimEnd('\')) {
				Write-Host "  note:       $($found.Source) runs instead, since it comes first on your PATH"
			} else {
				Write-Host "  note:       $dir is not on your PATH; to run strux by name, add it and open a new terminal:"
				Write-Host "                [Environment]::SetEnvironmentVariable('Path', '$dir;' + [Environment]::GetEnvironmentVariable('Path', 'User'), 'User')"
			}
		}
	} finally {
		Remove-Item -LiteralPath $tmp, $staged -Recurse -Force -ErrorAction SilentlyContinue
	}
}
