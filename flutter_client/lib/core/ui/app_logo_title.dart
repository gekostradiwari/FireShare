import 'package:flutter/material.dart';
import 'package:flutter_svg/flutter_svg.dart';

/// Titolo per le AppBar: logo FireShare + testo.
class AppLogoTitle extends StatelessWidget {
  final String title;
  final double logoSize;
  const AppLogoTitle(this.title, {super.key, this.logoSize = 30});

  @override
  Widget build(BuildContext context) {
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        SvgPicture.asset(
          'lib/resources/fireshare_logo.svg',
          width: logoSize,
          height: logoSize,
        ),
        const SizedBox(width: 8),
        Flexible(child: Text(title, overflow: TextOverflow.ellipsis)),
      ],
    );
  }
}
