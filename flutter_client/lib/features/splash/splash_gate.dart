import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter_svg/flutter_svg.dart';

const _bg = Color(0xFF0E1726);
const _gold = Color(0xFFFFC83D);

/// Mostra la splash animata (pioggia di 0/1 + logo + scritta) all'avvio,
/// poi sfuma verso l'app vera. Un tocco la salta.
class SplashGate extends StatefulWidget {
  final Widget child;
  const SplashGate({super.key, required this.child});

  @override
  State<SplashGate> createState() => _SplashGateState();
}

class _SplashGateState extends State<SplashGate>
    with SingleTickerProviderStateMixin {
  late final AnimationController _c;
  bool _done = false;

  @override
  void initState() {
    super.initState();
    _c = AnimationController(
      vsync: this,
      duration: const Duration(milliseconds: 2800),
    )
      ..addStatusListener((s) {
        if (s == AnimationStatus.completed) _finish();
      })
      ..forward();
  }

  void _finish() {
    if (!mounted || _done) return;
    setState(() => _done = true);
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AnimatedSwitcher(
      duration: const Duration(milliseconds: 450),
      child: _done
          ? KeyedSubtree(key: const ValueKey('app'), child: widget.child)
          : GestureDetector(
              key: const ValueKey('splash'),
              behavior: HitTestBehavior.opaque,
              onTap: _finish,
              child: _SplashView(animation: _c),
            ),
    );
  }
}

class _SplashView extends StatelessWidget {
  final Animation<double> animation;
  const _SplashView({required this.animation});

  static double _seg(double t, double a, double b,
      [Curve curve = Curves.easeOut]) {
    return curve.transform(((t - a) / (b - a)).clamp(0.0, 1.0));
  }

  @override
  Widget build(BuildContext context) {
    return Material(
      color: _bg,
      child: AnimatedBuilder(
        animation: animation,
        builder: (context, _) {
          final t = animation.value;
          final rain = _seg(t, 0.0, 0.25);
          final logoFade = _seg(t, 0.05, 0.25);
          final logoScale = _seg(t, 0.05, 0.40, Curves.easeOutBack);
          final titleIn = _seg(t, 0.35, 0.65);
          final tagIn = _seg(t, 0.55, 0.85);
          final pulse = 0.8 + 0.2 * math.sin(t * math.pi * 4);

          return Stack(
            fit: StackFit.expand,
            children: [
              Opacity(
                opacity: rain,
                child: CustomPaint(painter: _RainPainter(t * 2.8)),
              ),
              // alone dorato dietro la nuvola
              Center(
                child: Opacity(
                  opacity: logoFade,
                  child: Container(
                    width: 340,
                    height: 340,
                    decoration: BoxDecoration(
                      shape: BoxShape.circle,
                      gradient: RadialGradient(
                        colors: [
                          _gold.withOpacity(0.26 * pulse),
                          _gold.withOpacity(0.0),
                        ],
                      ),
                    ),
                  ),
                ),
              ),
              // logo (stessa posizione della splash nativa: centro schermo, 180dp)
              Center(
                child: Opacity(
                  opacity: logoFade,
                  child: Transform.scale(
                    scale: 0.55 + 0.45 * logoScale,
                    child: SvgPicture.asset(
                      'lib/resources/fireshare_logo.svg',
                      width: 180,
                      height: 180,
                    ),
                  ),
                ),
              ),
              // scritta FireShare sotto il logo
              Center(
                child: Transform.translate(
                  offset: Offset(0, 128 + 14 * (1 - titleIn)),
                  child: Opacity(
                    opacity: titleIn,
                    child: ShaderMask(
                      blendMode: BlendMode.srcIn,
                      shaderCallback: (rect) => const LinearGradient(
                        colors: [Color(0xFFFFF3A1), Color(0xFFF5A623)],
                      ).createShader(rect),
                      child: const Text(
                        'FireShare',
                        style: TextStyle(
                          color: Colors.white,
                          fontSize: 36,
                          fontWeight: FontWeight.w800,
                          letterSpacing: 1.5,
                        ),
                      ),
                    ),
                  ),
                ),
              ),
              Center(
                child: Transform.translate(
                  offset: Offset(0, 172 + 10 * (1 - tagIn)),
                  child: Opacity(
                    opacity: tagIn * 0.75,
                    child: const Text(
                      'Il tuo cloud personale',
                      style: TextStyle(
                        color: Colors.white,
                        fontSize: 14,
                        letterSpacing: 1.2,
                      ),
                    ),
                  ),
                ),
              ),
            ],
          );
        },
      ),
    );
  }
}

/// Pioggia digitale di 0 e 1 dorati (richiama il logo originale).
class _RainPainter extends CustomPainter {
  final double seconds;
  _RainPainter(this.seconds);

  static const _levels = [0.06, 0.14, 0.26, 0.45, 0.95];
  static final List<TextPainter> _glyphs = _buildGlyphs();

  static List<TextPainter> _buildGlyphs() {
    final out = <TextPainter>[];
    for (final ch in const ['0', '1']) {
      for (final a in _levels) {
        out.add(
          TextPainter(
            text: TextSpan(
              text: ch,
              style: TextStyle(
                color: _gold.withOpacity(a),
                fontSize: 18,
                fontWeight: FontWeight.bold,
                fontFamily: 'monospace',
              ),
            ),
            textDirection: TextDirection.ltr,
          )..layout(),
        );
      }
    }
    return out;
  }

  @override
  void paint(Canvas canvas, Size size) {
    const cell = 26.0;
    final cols = (size.width / cell).floor();
    final rows = (size.height / cell).ceil();
    final left = (size.width - cols * cell) / 2;
    for (var c = 0; c < cols; c++) {
      final rnd = math.Random(c * 7919 + 13);
      final speed = 4 + rnd.nextDouble() * 6; // celle al secondo
      final phase = rnd.nextDouble() * (rows + 10);
      final trail = 6 + rnd.nextInt(6);
      final head = (phase + seconds * speed) % (rows + trail + 4);
      final x = left + c * cell + (cell - 10) / 2;
      for (var k = 0; k < trail; k++) {
        final row = (head - k).floor();
        if (row < 0 || row >= rows) continue;
        final lvl = k == 0 ? 4 : math.max(0, 3 - (k * 4) ~/ trail);
        final flick = k == 0 ? (seconds * 8).floor() : 0;
        final bit = (row * 31 + c * 17 + flick) & 1;
        _glyphs[bit * _levels.length + lvl]
            .paint(canvas, Offset(x, row * cell));
      }
    }
  }

  @override
  bool shouldRepaint(covariant _RainPainter old) => old.seconds != seconds;
}
