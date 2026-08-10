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

VSINSTALL = os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(os.path.normpath(VCVARS)))))
print(f"WEASEL={WEASEL}")
print(f"BOOST_ROOT={BOOST_ROOT}")
print(f"BOOST_LIBRARYDIR={BOOST_LIBDIR}")
print(f"VCVARS={VCVARS}")
print(f"VSINSTALL={VSINSTALL}")

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


# Write a custom env.bat. We use the Ninja generator (same as upstream
# rime/librime's own Windows CI): it relies on the MSVC env set up by
# vcvarsall below and does NOT need Visual Studio instance discovery,
# which fails on CI runners ("could not find any instance of Visual
# Studio"). ARCH / PLATFORM_TOOLSET must stay unset: Ninja rejects -A/-T.
env_bat = os.path.join(LIBRIME, "env.bat")
env_bat_content = f'''set RIME_ROOT=%CD%
if not defined BOOST_ROOT set BOOST_ROOT={BOOST_ROOT}
set CMAKE_GENERATOR=Ninja
'''
with open(env_bat, "w", encoding="ascii", newline="\r\n") as f:
    f.write(env_bat_content)
print(f"Wrote custom env.bat: {env_bat}")

# Patch librime/build.bat so the -G flag is quoted (avoids cmd arg splitting).
build_bat_path = os.path.join(LIBRIME, "build.bat")
with open(build_bat_path, "r", encoding="utf-8", errors="replace") as f:
    bb = f.read()
patched = bb.replace("-G%CMAKE_GENERATOR%", "-G\"%CMAKE_GENERATOR%\"")
if patched != bb:
    with open(build_bat_path, "w", encoding="utf-8", newline="\r\n") as f:
        f.write(patched)
    print("Patched librime/build.bat: -G now quoted")
else:
    print("build.bat already patched or pattern missing")

# Step 1: librime deps (glog, leveldb, marisa, opencc, yaml-cpp)
log_deps = os.path.join(WEASEL, "_log_librime_deps.txt")
deps_script = f'''@echo off
call "{VCVARS}" x64
cd /d "{LIBRIME}"
set BOOST_ROOT={BOOST_ROOT}
set BOOST_LIBRARYDIR={BOOST_LIBDIR}
echo ==== DIAG cmake ====
cmake --version
echo ==== DIAG ninja ====
ninja --version
echo ==== DIAG env ====
echo VSINSTALLDIR=%VSINSTALLDIR%
echo CMAKE_GENERATOR=%CMAKE_GENERATOR%
echo ==== DIAG END ====
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
