"""Read original source archives as inert data; never extract or execute them."""
from pathlib import PurePosixPath
import hashlib
import posixpath
import stat
import tarfile
import zipfile


def sha(path):
    h = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


SOURCE_SUFFIXES = {".go", ".c", ".cc", ".cpp", ".h", ".js", ".ts", ".py", ".rs", ".sh", ".proto", ".java", ".json"}


def safe_name(name):
    parts = PurePosixPath(name).parts
    if not name or "\\" in name or name.startswith("/") or ".." in parts or ".git" in parts:
        raise ValueError("unsafe archive path: " + name)


def audit_archive(path):
    files, links, unsafe_links, submodules = [], [], [], []
    def record(name, size, preview=b""):
        safe_name(name)
        if preview.startswith(b"version https://git-lfs.github.com/spec/v1\n"):
            raise ValueError("unresolved Git LFS source pointer: " + name)
        if PurePosixPath(name).name == ".gitmodules" and b"[submodule" in preview:
            submodules.append(name)
        files.append((name, size))
    if path.suffix == ".zip":
        with zipfile.ZipFile(path) as archive:
            for member in archive.infolist():
                safe_name(member.filename)
                if stat.S_ISLNK(member.external_attr >> 16):
                    raise ValueError("module ZIP contains a symlink")
                if not member.is_dir():
                    preview = archive.read(member) if member.file_size <= 4096 else b""
                    record(member.filename, member.file_size, preview)
    else:
        with tarfile.open(path, "r|*") as archive:
            for member in archive:
                safe_name(member.name)
                if member.issym() or member.islnk():
                    target = member.linkname
                    resolved = posixpath.normpath(posixpath.join(posixpath.dirname(member.name), target)) if member.issym() else target
                    try:
                        safe_name(resolved)
                        if resolved.split("/")[0] != member.name.split("/")[0]:
                            raise ValueError("link leaves source root")
                    except ValueError:
                        unsafe_links.append([member.name, target])
                    links.append([member.name, target])
                elif member.isfile():
                    preview = archive.extractfile(member).read() if member.size <= 4096 else b""
                    record(member.name, member.size, preview)
                elif not member.isdir():
                    raise ValueError("archive contains a device or special entry: " + member.name)
    return {"sha256":sha(path), "bytes":path.stat().st_size, "files":len(files), "source_bytes":sum(size for _,size in files), "license_files":[name for name,_ in files if any(w in PurePosixPath(name).name.lower() for w in ("license","licence","copying","notice","copyright","patents"))], "source_files":sum(PurePosixPath(n).suffix in SOURCE_SUFFIXES for n,_ in files), "symlinks":links, "unsafe_symlinks":unsafe_links, "submodule_manifests":submodules}
