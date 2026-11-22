param(
  [string]$OutputDir = (Join-Path $PSScriptRoot 'goofish_extension\icons')
)

Add-Type -AssemblyName System.Drawing

function New-RoundedRectanglePath {
  param(
    [float]$X,
    [float]$Y,
    [float]$Width,
    [float]$Height,
    [float]$Radius
  )
  $path = [System.Drawing.Drawing2D.GraphicsPath]::new()
  $diameter = $Radius * 2
  $path.AddArc($X, $Y, $diameter, $diameter, 180, 90)
  $path.AddArc($X + $Width - $diameter, $Y, $diameter, $diameter, 270, 90)
  $path.AddArc($X + $Width - $diameter, $Y + $Height - $diameter, $diameter, $diameter, 0, 90)
  $path.AddArc($X, $Y + $Height - $diameter, $diameter, $diameter, 90, 90)
  $path.CloseFigure()
  return $path
}

function New-IconBitmap {
  param([int]$RenderSize = 512)
  $bitmap = [System.Drawing.Bitmap]::new(
    $RenderSize,
    $RenderSize,
    [System.Drawing.Imaging.PixelFormat]::Format32bppArgb
  )
  $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
  try {
    $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
    $graphics.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
    $graphics.Clear([System.Drawing.Color]::Transparent)

    $scale = $RenderSize / 512.0
    $graphics.ScaleTransform($scale, $scale)

    $backgroundRect = [System.Drawing.RectangleF]::new(12, 12, 488, 488)
    $backgroundPath = New-RoundedRectanglePath 12 12 488 488 108
    try {
      $gradient = [System.Drawing.Drawing2D.LinearGradientBrush]::new(
        $backgroundRect,
        [System.Drawing.ColorTranslator]::FromHtml('#FFE272'),
        [System.Drawing.ColorTranslator]::FromHtml('#EFA700'),
        45.0
      )
      try {
        $graphics.FillPath($gradient, $backgroundPath)
      } finally {
        $gradient.Dispose()
      }
    } finally {
      $backgroundPath.Dispose()
    }

    $ink = [System.Drawing.ColorTranslator]::FromHtml('#172033')
    $fishBody = [System.Drawing.Drawing2D.GraphicsPath]::new()
    try {
      $fishBody.StartFigure()
      $fishBody.AddBezier(142, 256, 181, 205, 239, 185, 301, 195)
      $fishBody.AddBezier(301, 195, 354, 203, 396, 229, 428, 256)
      $fishBody.AddBezier(428, 256, 396, 283, 354, 309, 301, 317)
      $fishBody.AddBezier(301, 317, 239, 327, 181, 307, 142, 256)
      $fishBody.CloseFigure()

      $tail = [System.Drawing.PointF[]]@(
        [System.Drawing.PointF]::new(158, 256),
        [System.Drawing.PointF]::new(78, 199),
        [System.Drawing.PointF]::new(55, 207),
        [System.Drawing.PointF]::new(69, 256),
        [System.Drawing.PointF]::new(55, 305),
        [System.Drawing.PointF]::new(78, 313)
      )
      $fishBody.AddPolygon($tail)
      $fishBody.FillMode = [System.Drawing.Drawing2D.FillMode]::Winding

      $inkBrush = [System.Drawing.SolidBrush]::new($ink)
      try {
        $graphics.FillPath($inkBrush, $fishBody)
      } finally {
        $inkBrush.Dispose()
      }
    } finally {
      $fishBody.Dispose()
    }

    $eyeBrush = [System.Drawing.SolidBrush]::new(
      [System.Drawing.ColorTranslator]::FromHtml('#FFF3BB')
    )
    try {
      $graphics.FillEllipse($eyeBrush, 352, 222, 36, 36)
    } finally {
      $eyeBrush.Dispose()
    }

    $pupilBrush = [System.Drawing.SolidBrush]::new($ink)
    try {
      $graphics.FillEllipse($pupilBrush, 366, 232, 13, 13)
    } finally {
      $pupilBrush.Dispose()
    }
  } finally {
    $graphics.Dispose()
  }
  return $bitmap
}

New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null

foreach ($size in @(16, 32, 48, 128)) {
  $renderSize = if ($size -le 32) { 256 } else { 512 }
  $source = New-IconBitmap -RenderSize $renderSize
  $target = [System.Drawing.Bitmap]::new(
    $size,
    $size,
    [System.Drawing.Imaging.PixelFormat]::Format32bppArgb
  )
  try {
    $graphics = [System.Drawing.Graphics]::FromImage($target)
    try {
      $graphics.Clear([System.Drawing.Color]::Transparent)
      $graphics.CompositingMode = [System.Drawing.Drawing2D.CompositingMode]::SourceCopy
      $graphics.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
      $graphics.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
      $graphics.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
      $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
      $graphics.DrawImage($source, 0, 0, $size, $size)
    } finally {
      $graphics.Dispose()
    }
    $target.Save(
      (Join-Path $OutputDir ("icon{0}.png" -f $size)),
      [System.Drawing.Imaging.ImageFormat]::Png
    )
  } finally {
    $target.Dispose()
    $source.Dispose()
  }
}

Write-Host ("Generated extension icons in {0}" -f $OutputDir)
