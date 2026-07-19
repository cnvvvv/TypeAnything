# Single-file TSF shim DLL — compile with mingw-w64
# Usage:
#   mingw32-make.exe        (debug build)
#   mingw32-make.exe RELEASE=1  (strip + optimize)

CXX     ?= x86_64-w64-mingw32-g++
CXXFLAGS = -DUNICODE -D_UNICODE -std=c++17 -Os -s
LDFLAGS  = -shared -static-libgcc -static-libstdc++ \
           -lole32 -luuid \
           -Wl,--out-implib,libWeaselTSFShim.a

TARGET = WeaselTSFShim.dll
SRCS = tsfshim.cpp

.PHONY: all clean

all: $(TARGET)

$(TARGET): $(SRCS)
	$(CXX) -o $@ $^ $(CXXFLAGS) $(LDFLAGS)
	@echo "=== Build complete: $(TARGET) ==="
	@ls -lh $(TARGET)

clean:
	rm -f $(TARGET) libWeaselTSFShim.a
