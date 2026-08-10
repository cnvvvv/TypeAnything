"""Build pipeline - builds librime deps + librime itself.

Steps:
  1. Build librime deps (glog/leveldb/marisa/opencc/yaml-cpp) via CMake
  2. Build librime itself (rime.dll)
"""

import os
import subprocess
import sys

# Auto-detect paths relative to this script
WEASEL = os.path.dirname(os.path.abspath(__file__))
LIBRIME = os.path.join(WEASEL, "librime")
BOOST_ROOT = os.environ.get("BOOST_ROOT", r"C:\local\boost_1_84_0")
BOOST_LIBDIR = os.environ.get("BOOST_LIBRARYDIR", os.path.join(BOOST_ROOT, "lib64-msvc-14.3"))

# Auto-detect vcvarsall.bat via vswhere
_vswhere = os.path.join(
    os.environ.get("ProgramFiles(x86)", r"C:\Program Files (x86)"),
    "Microsoft Visual Studio", "Installer", "vswhere.exe"
)
if os.path.exists(_vswhere):
    _vsPath = subprocess.check_output(
        [_vswhere, "-latest", "-property", "installationPath"],
        text=True
    ).strip()
    VCVARS = os.path.join(_vsPath, "VC", "Auxiliary", "Build", "vcvarsall.bat")
    VSINSTALLDIR = _vsPath + "\\"
    VCINSTALLDIR = os.path.join(_vsPath, "VC") + "\\"
    _cmakeNinja = os.path.join(_vsPath, "Common7", "IDE", "CommonExtensions",
                               "Microsoft", "CMake", "Ninja")
else:
    VCVARS = r"C:\Program Files (x86)\Microsoft Visual Studio\2022\BuildTools\VC\Auxiliary\Build\vcvarsall.bat"
    VSINSTALLDIR = r"C:\Program Files (x86)\Microsoft Visual Studio\2022\BuildTools\\"
    VCINSTALLDIR = r"C:\Program Files (x86)\Microsoft Visual Studio\2022\BuildTools\VC\\"
    _cmakeNinja = r"C:\Program Files (x86)\Microsoft Visual Studio\2022\BuildTools\Common7\IDE\CommonExtensions\Microsoft\CMake\Ninja"

print(f"WEASEL={WEASEL}")
print(f"BOOST_ROOT={BOOST_ROOT}")
print(f"BOOST_LIBRARYDIR={BOOST_LIBDIR}")
print(f"VCVARS={VCVARS}")

clean_env = {
    "PATH": (
        r"C:\Windows\System32;C:\Windows;C:\Windows\System32\Wbem;"
        + os.path.join(os.environ.get("ProgramFiles(x86)", r"C:\Program Files (x86)"),
                       "Microsoft Visual Studio", "Installer") + ";"
        + _cmakeNinja + ";"
        + r"C:\Program Files\Git\cmd" + ";"
        + r"C:\Program Files\CMake\bin" + ";"
        + os.path.expanduser(r"~\scoop\shims")
    ),
    "SystemRoot": r"C:\Windows",
    "ComSpec": r"C:\Windows\System32\cmd.exe",
    "USERPROFILE": os.environ.get("USERPROFILE", os.path.expanduser("~")),
    "LOCALAPPDATA": os.environ.get("LOCALAPPDATA", os.path.join(os.path.expanduser("~"), "AppData", "Local")),
    "APPDATA": os.environ.get("APPDATA", os.path.join(os.path.expanduser("~"), "AppData", "Roaming")),
    "TEMP": os.environ.get("TEMP", r"C:\Windows\Temp"),
    "TMP": os.environ.get("TMP", r"C:\Windows\Temp"),
    "NUMBER_OF_PROCESSORS": os.environ.get("NUMBER_OF_PROCESSORS", "8"),
    "PROCESSOR_ARCHITECTURE": os.environ.get("PROCESSOR_ARCHITECTURE", "AMD64"),
    "BOOST_ROOT": BOOST_ROOT,
    "BOOST_LIBRARYDIR": os.environ.get("BOOST_LIBRARYDIR", os.path.join(BOOST_ROOT, "lib64-msvc-14.3")),
    "VSINSTALLDIR": VSINSTALLDIR,
    "VCINSTALLDIR": VCINSTALLDIR,
    "VisualStudioVersion": "17.0",
}


def run_bat(label, script, cwd, log_path):
    path = os.path.join(cwd, "_run_step.bat")
    with open(path, "w", encoding="ascii", newline="\r\n") as f:
        f.write(script)
    print(f"\n=== {label} ===  cwd={cwd}\nlog: {log_path}")
    sys.stdout.flush()
    with open(log_path, "wb") as logf:
        proc = subprocess.run(
            ["cmd.exe", "/D", "/C", path],
            cwd=cwd,
            env=clean_env,
            shell=False,
            stdout=logf,
            stderr=subprocess.STDOUT,
        )
    print(f"=== {label} exit {proc.returncode} ===")
    sys.stdout.flush()
    return proc.returncode


# Step 1: librime deps (glog, leveldb, marisa, opencc, yaml-cpp)
log_deps = os.path.join(WEASEL, "_log_librime_deps.txt")
deps_script = f'''@echo off
call "{VCVARS}" x64
cd /d "{LIBRIME}"
set BOOST_ROOT={BOOST_ROOT}
set BOOST_LIBRARYDIR={BOOST_LIBDIR}
set CMAKE_GENERATOR=Visual Studio 17 2022
set ARCH=x64
set PLATFORM_TOOLSET=v143
call .\\build.bat deps
'''
rc = run_bat("librime deps", deps_script, LIBRIME, log_deps)
if rc != 0:
    print(f"deps failed rc={rc}, see {log_deps}")
    # Print last 80 lines of log for CI debugging
    try:
        with open(log_deps, "rb") as f:
            text = f.read().decode("utf-8", errors="replace")
        lines = text.strip().split("\n")
        print("--- deps log (last 80 lines) ---")
        for line in lines[-80:]:
            print("  " + line.rstrip())
    except Exception as e:
        print(f"  Could not read log: {e}")
    sys.exit(rc)

# Step 2: librime itself (rime.dll)
log_rime = os.path.join(WEASEL, "_log_librime.txt")
rime_script = f'''@echo off
call "{VCVARS}" x64
cd /d "{LIBRIME}"
set BOOST_ROOT={BOOST_ROOT}
set BOOST_LIBRARYDIR={BOOST_LIBDIR}
set CMAKE_GENERATOR=Visual Studio 17 2022
set ARCH=x64
set PLATFORM_TOOLSET=v143
call .\\build.bat librime
'''
rc = run_bat("librime", rime_script, LIBRIME, log_rime)
if rc != 0:
    print(f"librime failed rc={rc}, see {log_rime}")
    try:
        with open(log_rime, "rb") as f:
            text = f.read().decode("utf-8", errors="replace")
        lines = text.strip().split("\n")
        print("--- librime log (last 80 lines) ---")
        for line in lines[-80:]:
            print("  " + line.rstrip())
    except Exception as e:
        print(f"  Could not read log: {e}")
    sys.exit(rc)

print("\n=== librime built. Next: build weasel UI ===")
