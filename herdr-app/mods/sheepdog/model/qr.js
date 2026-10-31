// The pairing QR's module matrix, kept pure for `make test-panel`.
// `/api/pair/qr.json` answers {size, modules}, where every module row is a
// string of "0" (light) and "1" (dark). The quiet zone is already included.

export function qrRows(matrix) {
  const size = Number(matrix?.size);
  const modules = matrix?.modules;
  if (!Number.isInteger(size) || size < 1 || !Array.isArray(modules) || modules.length !== size) {
    throw new Error("The daemon did not answer a square pairing code.");
  }
  return modules.map((row) => {
    if (typeof row !== "string" || row.length !== size || /[^01]/.test(row)) {
      throw new Error("The daemon's pairing code has a bad row.");
    }
    return row.split("");
  });
}
